//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// compatImagePull drives testcontainers' PullImage, which calls
// POST /images/create. It skips when containerd has no registry egress, which
// is an environment property, never a pass.
func compatImagePull(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	requireRegistryEgress(t)

	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()
	reference := envOr(envFixtureImage, defaultFixtureImage)

	provider := testcontainersProvider(t)
	if err := provider.PullImage(ctx, reference); err != nil {
		t.Fatalf("testcontainers PullImage(%s): %v\n--- daemon logs ---\n%s", reference, err, daemon.Logs())
	}
	t.Logf("testcontainers PullImage(%s) succeeded", reference)

	inspect, err := daemon.Client().ImageInspect(ctx, reference)
	if err != nil {
		t.Fatalf("ImageInspect after pull: %v", err)
	}
	if inspect.ID == "" {
		t.Fatalf("pulled image inspect has no ID: %+v", inspect)
	}
	t.Logf("pull/inspect: id=%s tags=%v", inspect.ID, inspect.RepoTags)
}

// compatImageInspectConfig proves GET /images/{name}/json always renders a
// non-nil Config envelope: testcontainers-go dereferences
// Config.ExposedPorts on every create when the request names no ports.
func compatImageInspectConfig(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()

	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	var raw bytes.Buffer
	inspect, err := daemon.Client().ImageInspect(ctx, reference, client.ImageInspectWithRawResponse(&raw))
	if err != nil {
		t.Fatalf("ImageInspect(%s): %v\n--- daemon logs ---\n%s", reference, err, daemon.Logs())
	}
	if inspect.Config == nil {
		t.Fatal("ImageInspect.Config is nil; testcontainers-go dereferences Config.ExposedPorts and panics")
	}
	// An empty exposed-port set is omitted by the image config JSON tags, so
	// the guard is the Config object itself, exactly like Docker's envelope.
	if !strings.Contains(raw.String(), `"Config":{`) {
		t.Fatalf("raw image inspect has no Config object: %s", raw.String())
	}
	if len(inspect.RepoTags) == 0 {
		t.Fatal("ImageInspect carried no RepoTags for the fixture image")
	}
	t.Logf("image config envelope: id=%s exposed_ports=%d cmd=%v entrypoint=%v env=%v working_dir=%q",
		inspect.ID, len(inspect.Config.ExposedPorts), inspect.Config.Cmd, inspect.Config.Entrypoint,
		inspect.Config.Env, inspect.Config.WorkingDir)
}

// compatContainerWithoutExposedPorts is the regression for the nil Config
// panic: the request names no ports, so the library reads the image config to
// decide what to expose.
func compatContainerWithoutExposedPorts(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	image := ensureFixtureImage(t, daemon, daemon.cleanups)

	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()

	const marker = "dls-no-ports"
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:      image,
		Name:       uniqueName(t, "tc-noports"),
		Cmd:        []string{"sh", "-c", "echo " + marker + "; sleep 120"},
		WaitingFor: wait.ForLog(marker).WithStartupTimeout(compatWaitTimeout),
		Started:    true,
	})
	if err != nil {
		t.Fatalf("GenericContainer without ExposedPorts: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, container)

	state, err := container.State(ctx)
	if err != nil {
		t.Fatalf("Container.State: %v", err)
	}
	if !state.Running {
		t.Fatalf("container state = %+v, want running", state)
	}

	logs, err := container.Logs(ctx)
	if err != nil {
		t.Fatalf("Container.Logs: %v", err)
	}
	content, err := io.ReadAll(logs)
	_ = logs.Close()
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(content), marker) {
		t.Fatalf("logs = %q, want marker %q", string(content), marker)
	}
	t.Logf("no-exposed-ports container %s started and logged %q", container.GetContainerID(), strings.TrimSpace(string(content)))
}

// compatBuildPublishedPortsAndWaitForHTTP covers the BuildKit build path,
// automatic port publishing derived from the image config, the published-port
// mapping equality against inspect, wait.ForHTTP, container logs, and
// stop/remove including the image delete route.
func compatBuildPublishedPortsAndWaitForHTTP(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	configureTestcontainers(t, daemon)
	ctx, cancel := context.WithTimeout(context.Background(), compatTestTimeout)
	defer cancel()
	api := daemon.Client()

	repo := uniqueName(t, "tc-built")
	tag := repo + ":latest"
	container := compatBuildAndStartHTTPContainer(ctx, t, daemon, repo, tag)

	compatAssertBuiltImageConfig(ctx, t, api, tag)
	mappedPort := compatAssertPublishedPort(ctx, t, api, container)
	compatAssertHTTPThroughPort(ctx, t, mappedPort)
	compatAssertHelperStartupLog(ctx, t, container)
	compatAssertTerminateRemovesBuiltImage(ctx, t, api, daemon, container, tag)
}

// compatBuildAndStartHTTPContainer builds the fixture context through
// testcontainers and waits for the helper's HTTP endpoint.
func compatBuildAndStartHTTPContainer(ctx context.Context, t *testing.T, daemon *daemonProcess, repo, tag string) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Context:    fixtureContextDir(ctx, t),
		Repo:       repo,
		Tag:        "latest",
		Name:       uniqueName(t, "tc-http"),
		WaitingFor: wait.ForHTTP("/").WithPort("8080/tcp").WithStartupTimeout(compatWaitTimeout),
		Started:    true,
	})
	if err != nil {
		t.Fatalf("GenericContainer(FromDockerfile, wait.ForHTTP): %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	testcontainers.CleanupContainer(t, container)
	t.Logf("built and started %s from %s with wait.ForHTTP", container.GetContainerID(), tag)
	return container
}

// compatAssertBuiltImageConfig pins the built image's config envelope and its
// appearance in the image list.
func compatAssertBuiltImageConfig(ctx context.Context, t *testing.T, api *client.Client, tag string) {
	t.Helper()
	inspect, err := api.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatalf("ImageInspect(%s): %v", tag, err)
	}
	if inspect.Config == nil {
		t.Fatal("built image Config is nil")
	}
	if _, ok := inspect.Config.ExposedPorts["8080/tcp"]; !ok {
		t.Fatalf("built image Config.ExposedPorts = %v, want 8080/tcp from the Dockerfile EXPOSE", inspect.Config.ExposedPorts)
	}
	if !containsString(inspect.Config.Env, "DLS_COMPAT_ENV=present") {
		t.Fatalf("built image Config.Env = %v, want DLS_COMPAT_ENV=present", inspect.Config.Env)
	}
	if !containsString(inspect.Config.Entrypoint, "/dls-helper") {
		t.Fatalf("built image Config.Entrypoint = %v, want /dls-helper", inspect.Config.Entrypoint)
	}
	if !containsString(inspect.Config.Cmd, "serve") {
		t.Fatalf("built image Config.Cmd = %v, want serve 8080", inspect.Config.Cmd)
	}
	t.Logf("built image config: exposed=%v env=%v entrypoint=%v cmd=%v",
		inspect.Config.ExposedPorts, inspect.Config.Env, inspect.Config.Entrypoint, inspect.Config.Cmd)

	assertImageListed(ctx, t, api, tag, inspect.ID)
}

// assertImageListed proves one image ID is present in the daemon's image list.
func assertImageListed(ctx context.Context, t *testing.T, api *client.Client, tag, imageID string) {
	t.Helper()
	listing, err := api.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		t.Fatalf("ImageList: %v", err)
	}
	for _, item := range listing.Items {
		if item.ID == imageID {
			return
		}
	}
	t.Fatalf("built image %s (%s) missing from ImageList", tag, imageID)
}

// compatAssertPublishedPort pins the library-mapped port against both the
// daemon inspect and the library inspect, returning the mapped port.
func compatAssertPublishedPort(ctx context.Context, t *testing.T, api *client.Client, container testcontainers.Container) string {
	t.Helper()
	mapped, err := container.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatalf("Container.MappedPort(8080/tcp): %v", err)
	}
	if mapped.Port() == "" || mapped.Port() == "0" {
		t.Fatalf("MappedPort = %q, want a nonzero published port", mapped.Port())
	}
	daemonInspect, err := api.ContainerInspect(ctx, container.GetContainerID(), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	daemonPort := publishedHostPort(t, &daemonInspect.Container, 8080)
	if daemonPort != mapped.Port() {
		t.Fatalf("MappedPort = %q but daemon inspect reports %q", mapped.Port(), daemonPort)
	}
	tcInspect, err := container.Inspect(ctx)
	if err != nil {
		t.Fatalf("testcontainers Container.Inspect: %v", err)
	}
	if tcPort := publishedHostPort(t, tcInspect, 8080); tcPort != mapped.Port() {
		t.Fatalf("testcontainers inspect reports %q but MappedPort is %q", tcPort, mapped.Port())
	}
	t.Logf("published port: MappedPort=%s daemon_inspect=%s", mapped.Port(), daemonPort)
	return mapped.Port()
}

// compatAssertHTTPThroughPort fetches the helper through the CNI-published
// host port.
func compatAssertHTTPThroughPort(ctx context.Context, t *testing.T, mappedPort string) {
	t.Helper()
	httpClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	publishHost := hostPublishAddress(t)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(publishHost, mappedPort)+"/", nil)
	if err != nil {
		t.Fatalf("build GET http://%s:%s/: %v", publishHost, mappedPort, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("GET http://%s:%s/: %v", publishHost, mappedPort, err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read HTTP body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "dls-helper-ready" {
		t.Fatalf("HTTP GET mapped port = %d %q, want 200 dls-helper-ready", response.StatusCode, body)
	}
	t.Logf("HTTP through CNI portmap: status=%d body=%q", response.StatusCode, body)
}

// compatAssertHelperStartupLog pins the helper's startup line in the container
// logs.
func compatAssertHelperStartupLog(ctx context.Context, t *testing.T, container testcontainers.Container) {
	t.Helper()
	logs, err := container.Logs(ctx)
	if err != nil {
		t.Fatalf("Container.Logs: %v", err)
	}
	logContent, err := io.ReadAll(logs)
	_ = logs.Close()
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(logContent), "dls-helper serving on") {
		t.Fatalf("logs = %q, want the helper startup line", string(logContent))
	}
	t.Logf("container logs: %q", strings.TrimSpace(string(logContent)))
}

// compatAssertTerminateRemovesBuiltImage pins the terminate side effects on the
// container record and the built image tag.
func compatAssertTerminateRemovesBuiltImage(ctx context.Context, t *testing.T, api *client.Client, daemon *daemonProcess, container testcontainers.Container, tag string) {
	t.Helper()
	if err := container.Terminate(ctx, testcontainers.StopTimeout(5*time.Second)); err != nil {
		t.Fatalf("Container.Terminate: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if _, err := api.ContainerInspect(ctx, container.GetContainerID(), client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("ContainerInspect after Terminate = %v, want not found", err)
	}
	if _, err := api.ImageInspect(ctx, tag); !errdefs.IsNotFound(err) {
		t.Fatalf("ImageInspect after Terminate = %v, want not found (testcontainers removed the built image through DELETE /images/{name})", err)
	}
	if _, err := api.ImageRemove(ctx, tag, client.ImageRemoveOptions{Force: true, PruneChildren: true}); !errdefs.IsNotFound(err) {
		t.Fatalf("second ImageRemove = %v, want Docker 404 for an already removed image", err)
	}
	t.Logf("terminate removed container %s and built image %s (DELETE /images/{name}); second delete is 404", container.GetContainerID(), tag)
}
