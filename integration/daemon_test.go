//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	containerStateTimeout = 30 * time.Second
	execStateTimeout      = 30 * time.Second
	logMarkerTimeout      = 30 * time.Second
	buildTimeout          = 5 * time.Minute
)

// startReadyDaemon groups prerequisite detection and daemon startup so every
// test in this file either skips with the exact missing prerequisite or runs
// against a real daemon on an ephemeral socket.
func startReadyDaemon(t *testing.T) *daemonProcess {
	t.Helper()
	prerequisites := detectPrerequisites()
	prerequisites.require(t)
	return startDaemon(t, prerequisites)
}

// TestDaemonContainerLifecycle is the end-to-end lifecycle over the real
// socket: create, start, inspect, exec, logs, stop, remove.
func TestDaemonContainerLifecycle(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api := daemon.Client()

	const readyMarker = "dls-lifecycle-ready"
	name := uniqueName(t, "lifecycle")
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        reference,
			Cmd:          []string{"sh", "-c", "echo " + readyMarker + "; sleep 120"},
			AttachStdout: true,
			AttachStderr: true,
		},
		HostConfig:       &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		t.Fatalf("ContainerCreate(%s): %v\n--- daemon logs ---\n%s", name, err, daemon.Logs())
	}
	containerID := created.ID
	daemon.cleanups.add("remove container "+name, func(ctx context.Context) error {
		if _, err := api.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove container %s: %w", name, err)
		}
		return nil
	})
	t.Logf("created container %s (%s)", name, containerID)

	if _, err := api.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v\n--- daemon logs ---\n%s", containerID, err, daemon.Logs())
	}
	waitForContainerState(t, api, containerID, true)

	inspect, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect(%s): %v", containerID, err)
	}
	if inspect.Container.Name != "/"+name {
		t.Fatalf("inspect name = %q, want %q", inspect.Container.Name, "/"+name)
	}
	if inspect.Container.State == nil || !inspect.Container.State.Running {
		t.Fatalf("inspect state = %+v, want running", inspect.Container.State)
	}
	if inspect.Container.Config == nil || inspect.Container.Config.Image != reference {
		t.Fatalf("inspect config image = %+v, want %q", inspect.Container.Config, reference)
	}
	if inspect.Container.NetworkSettings == nil || len(inspect.Container.NetworkSettings.Networks) == 0 {
		t.Fatalf("inspect network settings = %+v, want a bridge attachment", inspect.Container.NetworkSettings)
	}
	bridge, ok := inspect.Container.NetworkSettings.Networks["bridge"]
	if !ok {
		t.Fatalf("inspect networks = %v, want a bridge attachment", inspect.Container.NetworkSettings.Networks)
	}
	if !bridge.IPAddress.IsValid() {
		t.Fatalf("bridge endpoint IP = %q, want a concrete address", bridge.IPAddress)
	}
	t.Logf("inspect: name=%s image=%s state=%s bridge_ip=%s", inspect.Container.Name, inspect.Container.Image, inspect.Container.State.Status, bridge.IPAddress)

	stdout, stderr := readContainerLogs(t, api, containerID, readyMarker)
	if !strings.Contains(stdout, readyMarker) {
		t.Fatalf("logs stdout = %q, want marker %q (stderr %q)", stdout, readyMarker, stderr)
	}
	t.Logf("logs: stdout=%q stderr=%q", strings.TrimSpace(stdout), strings.TrimSpace(stderr))

	execID := runExec(t, api, containerID)
	t.Logf("exec %s finished with exit code 7", execID)

	stopTimeout := 5
	started := time.Now()
	if _, err := api.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
		t.Fatalf("ContainerStop(%s): %v\n--- daemon logs ---\n%s", containerID, err, daemon.Logs())
	}
	waitForContainerState(t, api, containerID, false)
	stopped, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect after stop: %v", err)
	}
	t.Logf("stopped in %s: status=%s exit_code=%d", time.Since(started).Round(time.Millisecond), stopped.Container.State.Status, stopped.Container.State.ExitCode)

	if _, err := api.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatalf("ContainerRemove(%s): %v", containerID, err)
	}
	if _, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("ContainerInspect after remove error = %v, want not found", err)
	}
	t.Logf("container %s removed; inspect is 404", containerID)
}

// waitForContainerState polls inspect until the container reaches the wanted
// running state or the deadline expires.
func waitForContainerState(t *testing.T, api *client.Client, containerID string, wantRunning bool) {
	t.Helper()
	deadline := time.Now().Add(containerStateTimeout)
	var last *container.State
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		inspect, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		cancel()
		if err != nil {
			t.Fatalf("ContainerInspect(%s) while waiting: %v", containerID, err)
		}
		last = inspect.Container.State
		if last != nil && last.Running == wantRunning {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("container %s state = %+v, want running=%t", containerID, last, wantRunning)
}

// runExec executes a command inside the container, demuxes the attached
// output, and asserts the recorded exit code.
func runExec(t *testing.T, api *client.Client, containerID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), execStateTimeout)
	defer cancel()

	created, err := api.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          []string{"sh", "-c", "echo dls-exec-stdout; echo dls-exec-stderr >&2; exit 7"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	attach, err := api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatalf("ExecAttach: %v", err)
	}
	defer attach.Close()
	_ = attach.Conn.SetReadDeadline(time.Now().Add(execStateTimeout))

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		t.Fatalf("demux exec output: %v", err)
	}
	if !strings.Contains(stdout.String(), "dls-exec-stdout") {
		t.Fatalf("exec stdout = %q, want dls-exec-stdout", stdout.String())
	}
	if !strings.Contains(stderr.String(), "dls-exec-stderr") {
		t.Fatalf("exec stderr = %q, want dls-exec-stderr", stderr.String())
	}

	deadline := time.Now().Add(execStateTimeout)
	var inspect client.ExecInspectResult
	for time.Now().Before(deadline) {
		inspect, err = api.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			t.Fatalf("ExecInspect: %v", err)
		}
		if !inspect.Running && inspect.ExitCode == 7 {
			return created.ID
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("exec %s inspect = %+v, want exit code 7", created.ID, inspect)
	return ""
}

// readContainerLogs polls the container logs until the wanted marker appears
// and returns the demultiplexed stdout and stderr streams.
func readContainerLogs(t *testing.T, api *client.Client, containerID, marker string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(logMarkerTimeout)
	var stdout, stderr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stream, err := api.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		})
		if err != nil {
			cancel()
			t.Fatalf("ContainerLogs(%s): %v", containerID, err)
		}
		var out, errOut bytes.Buffer
		_, copyErr := stdcopy.StdCopy(&out, &errOut, stream)
		_ = stream.Close()
		cancel()
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			t.Fatalf("demux container logs: %v", copyErr)
		}
		stdout, stderr = out.String(), errOut.String()
		if strings.Contains(stdout, marker) {
			return stdout, stderr
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("container %s logs never contained %q: stdout=%q stderr=%q", containerID, marker, stdout, stderr)
	return "", ""
}
