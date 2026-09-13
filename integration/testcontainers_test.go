//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestTestcontainersGoContainerLifecycle drives the REAL
// github.com/testcontainers/testcontainers-go library against this daemon:
// DOCKER_HOST points at the ephemeral socket, Ryuk stays disabled, and a real
// container is started, logged, and stopped through the library.
func TestTestcontainersGoContainerLifecycle(t *testing.T) {
	daemon := startReadyDaemon(t)
	image := ensureFixtureImage(t, daemon, daemon.cleanups)

	// The MVP explicitly disables the reaper; prove the library agrees before
	// any provider is created. TestMain sets the environment variable so the
	// config singleton can never have read it as enabled.
	if config := testcontainers.ReadConfig(); !config.RyukDisabled {
		t.Fatalf("testcontainers Ryuk must be disabled, config=%+v", config)
	}

	t.Setenv("DOCKER_HOST", daemon.Host())
	// testcontainers' Moby client does not negotiate the API version, so pin
	// it to the version this daemon advertises.
	t.Setenv("DOCKER_API_VERSION", "1.44")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const marker = "dls-tc-marker"
	name := uniqueName(t, "tc")
	request := testcontainers.ContainerRequest{
		Image: image,
		Name:  name,
		Cmd:   []string{"sh", "-c", "echo " + marker + "; sleep 120"},
		Labels: map[string]string{
			"io.dockerdless.test": "testcontainers",
		},
		// A non-empty ExposedPorts list keeps testcontainers from dereferencing
		// ImageInspect.Config, which this MVP does not populate yet.
		ExposedPorts: []string{"80/tcp"},
		WaitingFor:   wait.ForLog(marker).WithStartupTimeout(90 * time.Second),
	}

	started := time.Now()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("testcontainers GenericContainer: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, container)

	containerID := container.GetContainerID()
	if containerID == "" {
		t.Fatal("testcontainers returned an empty container ID")
	}
	t.Logf("testcontainers container %s (%s) started in %s", name, containerID, time.Since(started).Round(time.Millisecond))

	listing, err := daemon.Client().ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatalf("ContainerList while checking for a reaper: %v", err)
	}
	for _, item := range listing.Items {
		if strings.Contains(item.Image, "ryuk") || hasReaperName(item.Names) {
			t.Fatalf("Ryuk reaper container present although Ryuk is disabled: %+v", item)
		}
	}

	logs, err := container.Logs(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.Logs: %v", err)
	}
	content, err := io.ReadAll(logs)
	_ = logs.Close()
	if err != nil {
		t.Fatalf("read testcontainers logs: %v", err)
	}
	if !strings.Contains(string(content), marker) {
		t.Fatalf("testcontainers logs = %q, want marker %q", string(content), marker)
	}
	t.Logf("testcontainers logs: %q", strings.TrimSpace(string(content)))

	state, err := container.State(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.State: %v", err)
	}
	if !state.Running {
		t.Fatalf("testcontainers container state = %+v, want running", state)
	}

	stopTimeout := 5 * time.Second
	stoppedAt := time.Now()
	if err := container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("testcontainers Container.Stop: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	state, err = container.State(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.State after stop: %v", err)
	}
	if state.Running {
		t.Fatalf("testcontainers container still running after Stop: %+v", state)
	}
	t.Logf("testcontainers container %s stopped in %s: status=%s exit_code=%d",
		containerID, time.Since(stoppedAt).Round(time.Millisecond), state.Status, state.ExitCode)
}

func hasReaperName(names []string) bool {
	for _, name := range names {
		if strings.HasPrefix(strings.TrimPrefix(name, "/"), "reaper_") {
			return true
		}
	}
	return false
}
