//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

func compatPrivilegedCreateRejected(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := daemon.Client().ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:latest"},
		HostConfig: &container.HostConfig{Privileged: true},
	})
	if err == nil {
		t.Fatal("privileged container create unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "HostConfig.Privileged is not supported") {
		t.Fatalf("privileged create error = %v, want Docker unsupported-field message", err)
	}
}

// compatWaitForExecAndExitCodes proves the exec route end to end through
// testcontainers' wait.ForExec, a nonzero exit code, and a failing wait
// strategy that must surface an error rather than a false pass.
func compatWaitForExecAndExitCodes(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	image := ensureFixtureImage(t, daemon, daemon.cleanups)

	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:      image,
		Name:       uniqueName(t, "tc-exec"),
		Cmd:        []string{"sh", "-c", "sleep 120"},
		WaitingFor: wait.ForExec([]string{"sh", "-c", "echo dls-exec-wait"}).WithStartupTimeout(compatWaitTimeout),
		Started:    true,
	})
	if err != nil {
		t.Fatalf("GenericContainer(wait.ForExec): %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, container)
	t.Logf("wait.ForExec succeeded on %s", container.GetContainerID())

	exitCode, output, err := container.Exec(ctx, []string{"sh", "-c", "echo dls-exec-out; echo dls-exec-err >&2; exit 7"}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("Container.Exec: %v", err)
	}
	if exitCode != 7 {
		t.Fatalf("Container.Exec exit code = %d, want 7", exitCode)
	}
	content, err := io.ReadAll(output)
	if err != nil {
		t.Fatalf("read exec output: %v", err)
	}
	if !strings.Contains(string(content), "dls-exec-out") || !strings.Contains(string(content), "dls-exec-err") {
		t.Fatalf("exec output = %q, want both captured streams", string(content))
	}
	t.Logf("exec exit code=%d output=%q", exitCode, strings.TrimSpace(string(content)))

	failingCtx, failingCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer failingCancel()
	failing, err := testcontainers.GenericContainer(failingCtx, testcontainers.GenericContainerRequest{
		Image:      image,
		Name:       uniqueName(t, "tc-exec-fail"),
		Cmd:        []string{"sh", "-c", "sleep 120"},
		WaitingFor: wait.ForExec([]string{"false"}).WithStartupTimeout(5 * time.Second),
		Started:    true,
	})
	if err == nil {
		testcontainers.CleanupContainer(t, failing)
		t.Fatal("wait.ForExec(false).WithExitCode(0) unexpectedly succeeded")
	}
	if failing != nil {
		testcontainers.CleanupContainer(t, failing)
	}
	t.Logf("failing wait.ForExec surfaced: %v", err)
}

// compatNetworkCreateInspectListRemove drives testcontainers' network provider
// and asserts the created network through both the library and the daemon's
// network list.
func compatNetworkCreateInspectListRemove(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()
	api := daemon.Client()

	name := uniqueName(t, "tc-net")
	network, err := testcontainers.GenericNetwork(ctx, testcontainers.GenericNetworkRequest{
		Name:   name,
		Driver: "bridge",
		Labels: map[string]string{"io.dockerdless.test": "compat"},
	})
	if err != nil {
		t.Fatalf("testcontainers GenericNetwork: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupNetwork(t, network)

	provider := testcontainersProvider(t)
	inspect, err := provider.GetNetwork(ctx, testcontainers.NetworkRequest{Name: name})
	if err != nil {
		t.Fatalf("testcontainers GetNetwork(%s): %v", name, err)
	}
	if inspect.Name != name || inspect.ID == "" {
		t.Fatalf("network inspect = %+v, want name %q and a non-empty ID", inspect, name)
	}

	listing, err := api.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}
	found := false
	for _, item := range listing.Items {
		if item.ID == inspect.ID || item.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created network %s (%s) missing from NetworkList", name, inspect.ID)
	}
	t.Logf("network created and listed: name=%s id=%s", inspect.Name, inspect.ID)

	if err := network.Remove(ctx); err != nil {
		t.Fatalf("testcontainers Network.Remove: %v", err)
	}
	if _, err := provider.GetNetwork(ctx, testcontainers.NetworkRequest{Name: name}); err == nil {
		t.Fatalf("network %s is still inspectable after removal", name)
	}
	t.Logf("network %s removed and no longer inspectable", name)
}

// compatContainerReuse drives ContainerList-based reuse: the second
// GenericContainer call must adopt the first container by name.
func compatContainerReuse(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	image := ensureFixtureImage(t, daemon, daemon.cleanups)

	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()

	const marker = "dls-reuse-marker"
	name := uniqueName(t, "tc-reuse")
	first, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:      image,
		Name:       name,
		Cmd:        []string{"sh", "-c", "echo " + marker + "; sleep 120"},
		WaitingFor: wait.ForLog(marker).WithStartupTimeout(compatWaitTimeout),
		Started:    true,
	})
	if err != nil {
		t.Fatalf("first GenericContainer: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, first)

	second, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:   image,
		Name:    name,
		Started: true,
		Reuse:   true,
	})
	if err != nil {
		t.Fatalf("reused GenericContainer: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, second)

	if first.GetContainerID() != second.GetContainerID() {
		t.Fatalf("reuse returned container %s, want the existing %s", second.GetContainerID(), first.GetContainerID())
	}
	state, err := second.State(ctx)
	if err != nil {
		t.Fatalf("reused Container.State: %v", err)
	}
	if !state.Running {
		t.Fatalf("reused container state = %+v, want running", state)
	}

	listing, err := daemon.Client().ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("name", "^"+name+"$"),
	})
	if err != nil {
		t.Fatalf("ContainerList(name=%s): %v", name, err)
	}
	if len(listing.Items) != 1 {
		t.Fatalf("ContainerList(name=%s) returned %d items, want exactly 1", name, len(listing.Items))
	}
	if listing.Items[0].ID != first.GetContainerID() {
		t.Fatalf("ContainerList returned %s, want %s", listing.Items[0].ID, first.GetContainerID())
	}
	t.Logf("reuse adopted container %s; ContainerList(name) returned it once", first.GetContainerID())
}

// compatContainerLifecycle drives create, start, log wait, logs, state, and
// stop through the library and asserts no reaper container ever appears.
func compatContainerLifecycle(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	image := ensureFixtureImage(t, daemon, daemon.cleanups)

	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()

	const marker = "dls-tc-marker"
	name := uniqueName(t, "tc")
	container := compatStartLifecycleContainer(ctx, t, daemon, image, name, marker)

	compatAssertNoReaper(ctx, t, daemon)
	compatAssertContainerLogsMarker(ctx, t, container, marker)
	compatAssertContainerRunning(ctx, t, container)
	compatStopContainer(ctx, t, daemon, container)
}

// compatStartLifecycleContainer creates the no-exposed-ports container, waits
// for its log marker, and returns it.
func compatStartLifecycleContainer(ctx context.Context, t *testing.T, daemon *daemonProcess, image, name, marker string) testcontainers.Container {
	t.Helper()
	request := testcontainers.ContainerRequest{
		Image: image,
		Name:  name,
		Cmd:   []string{"sh", "-c", "echo " + marker + "; sleep 120"},
		Labels: map[string]string{
			"io.dockerdless.test": "testcontainers",
		},
		// No ExposedPorts on purpose: testcontainers derives them from the
		// image inspect Config envelope (nil Config used to panic the library).
		WaitingFor: wait.ForLog(marker).WithStartupTimeout(compatWaitTimeout),
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
	return container
}

// compatAssertNoReaper proves Ryuk is disabled by rejecting any reaper-shaped
// container in the daemon's list.
func compatAssertNoReaper(ctx context.Context, t *testing.T, daemon *daemonProcess) {
	t.Helper()
	listing, err := daemon.Client().ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatalf("ContainerList while checking for a reaper: %v", err)
	}
	for _, item := range listing.Items {
		if strings.Contains(item.Image, "ryuk") || hasReaperName(item.Names) {
			t.Fatalf("Ryuk reaper container present although Ryuk is disabled: %+v", item)
		}
	}
}

// compatAssertContainerLogsMarker pins the container logs against the startup
// marker.
func compatAssertContainerLogsMarker(ctx context.Context, t *testing.T, container testcontainers.Container, marker string) {
	t.Helper()
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
}

// compatAssertContainerRunning pins the library-reported running state.
func compatAssertContainerRunning(ctx context.Context, t *testing.T, container testcontainers.Container) {
	t.Helper()
	state, err := container.State(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.State: %v", err)
	}
	if !state.Running {
		t.Fatalf("testcontainers container state = %+v, want running", state)
	}
}

// compatStopContainer stops the container and pins the library-reported
// stopped state and timings.
func compatStopContainer(ctx context.Context, t *testing.T, daemon *daemonProcess, container testcontainers.Container) {
	t.Helper()
	stopTimeout := 5 * time.Second
	stoppedAt := time.Now()
	if err := container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("testcontainers Container.Stop: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	state, err := container.State(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.State after stop: %v", err)
	}
	if state.Running {
		t.Fatalf("testcontainers container still running after Stop: %+v", state)
	}
	t.Logf("testcontainers container %s stopped in %s: status=%s exit_code=%d",
		container.GetContainerID(), time.Since(stoppedAt).Round(time.Millisecond), state.Status, state.ExitCode)
}

func hasReaperName(names []string) bool {
	for _, name := range names {
		if strings.HasPrefix(strings.TrimPrefix(name, "/"), "reaper_") {
			return true
		}
	}
	return false
}
