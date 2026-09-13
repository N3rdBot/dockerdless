//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const (
	daemonReadyTimeout = 30 * time.Second
	daemonStopTimeout  = 15 * time.Second
	daemonReadyPoll    = 100 * time.Millisecond
)

// daemonProcess is a real dockerdless binary running on an ephemeral socket
// with ephemeral CNI config/log directories.
type daemonProcess struct {
	namePrefix   string
	tempRoot     string
	socketPath   string
	cniConfigDir string

	containerdSocket string
	namespace        string

	cmd    *exec.Cmd
	logs   *lockedBuffer
	done   chan struct{}
	exitMu sync.Mutex
	exit   error

	client   *client.Client
	cleanups *cleanupRegistry

	stopOnce sync.Once
	stopErr  error
}

// startDaemon builds and starts the real daemon binary, waits for `_ping`,
// and registers the full cleanup chain (containers, bridges, process, socket)
// before any assertion can fail.
func startDaemon(t *testing.T, prerequisites prerequisites) *daemonProcess {
	t.Helper()
	binary := daemonBinary(t)

	tempRoot := t.TempDir()
	socketPath := filepath.Join(tempRoot, "dockerdless.sock")
	cniConfigDir := filepath.Join(tempRoot, "cni", "net.d")
	if err := os.MkdirAll(cniConfigDir, 0o755); err != nil {
		t.Fatalf("create ephemeral CNI config directory: %v", err)
	}

	logs := &lockedBuffer{}
	cmd := exec.Command(binary)
	cmd.Dir = tempRoot
	cmd.Env = append(os.Environ(),
		"DOCKERDLESS_SOCKET_PATH="+socketPath,
		"DOCKERDLESS_CONTAINERD_NAMESPACE="+prerequisites.namespace,
		"DOCKERDLESS_CONTAINERD_SOCKET="+prerequisites.containerdSocket,
		"DOCKERDLESS_CNI_CONFIG_DIR="+cniConfigDir,
		"DOCKERDLESS_CNI_PLUGIN_DIR="+prerequisites.cniPluginDir,
		"DOCKERDLESS_BUILDKIT_SOCKET="+prerequisites.buildkitSocket,
		"DOCKERDLESS_LOG_LEVEL=debug",
	)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the real dockerdless daemon: %v", err)
	}

	daemon := &daemonProcess{
		namePrefix:       fmt.Sprintf("dls-%d-", os.Getpid()),
		tempRoot:         tempRoot,
		socketPath:       socketPath,
		cniConfigDir:     cniConfigDir,
		containerdSocket: prerequisites.containerdSocket,
		namespace:        prerequisites.namespace,
		cmd:              cmd,
		logs:             logs,
		done:             make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		daemon.exitMu.Lock()
		daemon.exit = err
		daemon.exitMu.Unlock()
		close(daemon.done)
	}()

	// Cleanup steps run LIFO: test-registered resources first, then the
	// containerd sweep, then leftover bridges, and finally the daemon stop.
	daemon.cleanups = newCleanupRegistry(t)
	daemon.cleanups.add("stop dockerdless daemon", daemon.Stop)
	daemon.cleanups.add("delete leftover CNI bridge devices", func(context.Context) error {
		return deleteLeftoverBridges(cniConfigDir)
	})
	daemon.cleanups.add("sweep leftover adapter containers and images", func(ctx context.Context) error {
		return sweepAdapterState(ctx, prerequisites.containerdSocket, prerequisites.namespace, daemon.namePrefix)
	})

	if err := waitForDaemonReady(t.Context(), daemon); err != nil {
		t.Fatalf("dockerdless daemon never became ready: %v\n--- daemon logs ---\n%s", err, logs.String())
	}
	cli, err := client.New(client.WithHost(daemon.Host()), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create daemon API client: %v", err)
	}
	daemon.client = cli
	return daemon
}

func waitForDaemonReady(ctx context.Context, daemon *daemonProcess) error {
	ctx, cancel := context.WithTimeout(ctx, daemonReadyTimeout)
	defer cancel()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", daemon.socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: time.Second}

	ticker := time.NewTicker(daemonReadyPoll)
	defer ticker.Stop()
	var lastError error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dockerdless/_ping", nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastError = fmt.Errorf("GET /_ping returned HTTP %d", response.StatusCode)
		} else {
			lastError = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("last readiness error: %w", lastError)
		case <-daemon.done:
			return fmt.Errorf("daemon exited during startup")
		case <-ticker.C:
		}
	}
}

// Client returns the Moby-compatible API client bound to this daemon.
func (d *daemonProcess) Client() *client.Client { return d.client }

// Host returns the DOCKER_HOST value (unix://) for this daemon.
func (d *daemonProcess) Host() string { return "unix://" + d.socketPath }

// SocketPath returns the ephemeral socket path.
func (d *daemonProcess) SocketPath() string { return d.socketPath }

// CNIConfigDir returns the ephemeral CNI configuration directory.
func (d *daemonProcess) CNIConfigDir() string { return d.cniConfigDir }

// NamePrefix is the unique prefix every container created for this test
// binary must use so the leftover sweep can identify its own resources.
func (d *daemonProcess) NamePrefix() string { return d.namePrefix }

// Logs returns everything the daemon wrote to stdout/stderr so far.
func (d *daemonProcess) Logs() string { return d.logs.String() }

// Stop signals the daemon with SIGTERM, waits for a clean exit, and asserts
// that the socket file is gone. It is idempotent so both the test body and
// the cleanup registry can call it.
func (d *daemonProcess) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() {
		d.stopErr = d.stop(ctx)
	})
	return d.stopErr
}

func (d *daemonProcess) stop(ctx context.Context) error {
	if d.client != nil {
		_ = d.client.Close()
	}

	alreadyExited := false
	select {
	case <-d.done:
		alreadyExited = true
	default:
	}

	var problems []error
	if !alreadyExited {
		if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			problems = append(problems, fmt.Errorf("signal SIGTERM: %w", err))
		}
		select {
		case <-d.done:
		case <-time.After(daemonStopTimeout):
			_ = d.cmd.Process.Kill()
			<-d.done
			problems = append(problems, fmt.Errorf("daemon did not exit within %s after SIGTERM", daemonStopTimeout))
		}
	}

	d.exitMu.Lock()
	exitErr := d.exit
	d.exitMu.Unlock()
	if exitErr != nil {
		if alreadyExited {
			problems = append(problems, fmt.Errorf("daemon had already exited before stop: %w", exitErr))
		} else {
			problems = append(problems, fmt.Errorf("daemon exited with error after SIGTERM: %w", exitErr))
		}
	}

	if _, statErr := os.Stat(d.socketPath); !os.IsNotExist(statErr) {
		problems = append(problems, fmt.Errorf("socket %s still present after daemon exit (stat error %v)", d.socketPath, statErr))
	}
	return errors.Join(problems...)
}

// lockedBuffer makes the daemon's output safe to read while it is running.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
