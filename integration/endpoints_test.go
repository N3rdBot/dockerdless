//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// TestDaemonSystemEndpoints drives ping, version, and info over the real
// Unix socket.
func TestDaemonSystemEndpoints(t *testing.T) {
	daemon := startReadyDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api := daemon.Client()

	ping, err := api.Ping(ctx, client.PingOptions{})
	if err != nil {
		t.Fatalf("Ping: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if ping.APIVersion != "1.44" {
		t.Fatalf("Ping APIVersion = %q, want %q", ping.APIVersion, "1.44")
	}

	version, err := api.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatalf("ServerVersion: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if version.APIVersion != "1.44" {
		t.Fatalf("ServerVersion APIVersion = %q, want %q", version.APIVersion, "1.44")
	}
	if version.MinAPIVersion != "1.24" {
		t.Fatalf("ServerVersion MinAPIVersion = %q, want %q", version.MinAPIVersion, "1.24")
	}
	if version.Os != "linux" {
		t.Fatalf("ServerVersion Os = %q, want linux", version.Os)
	}
	t.Logf("version: version=%s api=%s min_api=%s os=%s arch=%s", version.Version, version.APIVersion, version.MinAPIVersion, version.Os, version.Arch)

	info, err := api.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Fatalf("Info: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if strings.TrimSpace(info.Info.ServerVersion) == "" {
		t.Fatalf("Info.ServerVersion is empty: %+v", info.Info)
	}
	if info.Info.OperatingSystem == "" {
		t.Fatalf("Info.OperatingSystem is empty: %+v", info.Info)
	}
	t.Logf("info: server_version=%s os=%s arch=%s containers=%d images=%d",
		info.Info.ServerVersion, info.Info.OperatingSystem, info.Info.Architecture,
		info.Info.Containers, info.Info.Images)
}

// TestDaemonImageInspectAndList proves the image store is reachable through
// the daemon and consistent between inspect and list.
func TestDaemonImageInspectAndList(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	api := daemon.Client()

	inspect, err := api.ImageInspect(ctx, reference)
	if err != nil {
		t.Fatalf("ImageInspect(%s): %v\n--- daemon logs ---\n%s", reference, err, daemon.Logs())
	}
	if inspect.ID == "" {
		t.Fatalf("ImageInspect(%s).ID is empty", reference)
	}
	if inspect.Os != "linux" {
		t.Fatalf("ImageInspect(%s).Os = %q, want linux", reference, inspect.Os)
	}
	if inspect.Architecture == "" {
		t.Fatalf("ImageInspect(%s).Architecture is empty", reference)
	}
	if inspect.Size <= 0 {
		t.Fatalf("ImageInspect(%s).Size = %d, want > 0", reference, inspect.Size)
	}

	list, err := api.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		t.Fatalf("ImageList: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	var found bool
	for _, item := range list.Items {
		if item.ID == inspect.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ImageList did not contain inspected image %s (%s)", reference, inspect.ID)
	}
	t.Logf("image: id=%s size=%d tags=%v digests=%v", inspect.ID, inspect.Size, inspect.RepoTags, inspect.RepoDigests)
}

// TestDaemonNetworkLifecycle lists the default networks, creates and inspects
// a custom bridge network, asserts its conflist on disk, and removes it.
func TestDaemonNetworkLifecycle(t *testing.T) {
	daemon := startReadyDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	api := daemon.Client()

	defaults, err := api.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		t.Fatalf("NetworkList: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	names := make(map[string]struct{}, len(defaults.Items))
	for _, item := range defaults.Items {
		names[item.Name] = struct{}{}
	}
	for _, want := range []string{"bridge", "host", "none"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("default network %q missing from %v", want, names)
		}
	}
	t.Logf("default networks: %v", names)

	name := uniqueName(t, "network")
	created, err := api.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge"})
	if err != nil {
		t.Fatalf("NetworkCreate(%s): %v\n--- daemon logs ---\n%s", name, err, daemon.Logs())
	}
	daemon.cleanups.add("remove network "+name, func(ctx context.Context) error {
		if _, err := api.NetworkRemove(ctx, created.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove network %s: %w", name, err)
		}
		return nil
	})

	inspect, err := api.NetworkInspect(ctx, created.ID, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("NetworkInspect(%s): %v", created.ID, err)
	}
	if inspect.Network.ID != created.ID {
		t.Fatalf("NetworkInspect ID = %q, want %q", inspect.Network.ID, created.ID)
	}
	if inspect.Network.Name != name {
		t.Fatalf("NetworkInspect Name = %q, want %q", inspect.Network.Name, name)
	}
	if inspect.Network.Driver != "bridge" {
		t.Fatalf("NetworkInspect Driver = %q, want bridge", inspect.Network.Driver)
	}
	conflistPath := filepath.Join(daemon.CNIConfigDir(), "dockerdless-"+name+".conflist")
	if _, err := os.Stat(conflistPath); err != nil {
		t.Fatalf("network conflist %s missing: %v", conflistPath, err)
	}
	t.Logf("network %s (%s) created; conflist=%s", name, created.ID, conflistPath)

	if _, err := api.NetworkRemove(ctx, created.ID, client.NetworkRemoveOptions{}); err != nil {
		t.Fatalf("NetworkRemove(%s): %v\n--- daemon logs ---\n%s", created.ID, err, daemon.Logs())
	}
	if _, err := os.Stat(conflistPath); !os.IsNotExist(err) {
		t.Fatalf("network conflist %s still present after removal (stat error %v)", conflistPath, err)
	}
	remaining, err := api.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		t.Fatalf("NetworkList after removal: %v", err)
	}
	for _, item := range remaining.Items {
		if item.Name == name {
			t.Fatalf("network %q still listed after removal", name)
		}
	}
	t.Logf("network %s removed; conflist deleted and list is clean", name)
}

// TestDaemonBuildFromFixture builds integration/fixtures through POST /build,
// then runs the built image and reads the marker file it copied in.
func TestDaemonBuildFromFixture(t *testing.T) {
	daemon := startReadyDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	api := daemon.Client()

	contextTar, fixturesDir := fixtureContextTar(t)
	tag := fmt.Sprintf("docker.io/library/%s:latest", uniqueName(t, "built"))
	build, err := api.ImageBuild(ctx, contextTar, client.ImageBuildOptions{
		Tags:   []string{tag},
		Remove: true,
	})
	if err != nil {
		t.Fatalf("ImageBuild(%s): %v\n--- daemon logs ---\n%s", tag, err, daemon.Logs())
	}
	defer func() { _ = build.Body.Close() }()
	stream, err := io.ReadAll(build.Body)
	if err != nil {
		t.Fatalf("read build stream: %v", err)
	}
	if bytes.Contains(stream, []byte(`"errorDetail"`)) {
		t.Fatalf("build stream carried errorDetail:\n%s", stream)
	}
	if !bytes.Contains(stream, []byte(`"aux"`)) {
		t.Fatalf("build stream carried no aux image id:\n%s", stream)
	}
	t.Logf("build fixture %s completed with %d stream bytes", fixturesDir, len(stream))

	daemon.cleanups.add("remove built image "+tag, func(ctx context.Context) error {
		return removeImageFromContainerd(ctx, daemon.containerdSocket, daemon.namespace, tag)
	})

	inspect, err := api.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatalf("ImageInspect(%s): %v", tag, err)
	}
	if inspect.ID == "" {
		t.Fatalf("built image has no ID: %+v", inspect)
	}
	var tagged bool
	for _, repoTag := range inspect.RepoTags {
		if repoTag == strings.TrimPrefix(tag, "docker.io/library/") || repoTag == tag {
			tagged = true
			break
		}
	}
	if !tagged {
		t.Fatalf("built image RepoTags = %v, want %q", inspect.RepoTags, tag)
	}

	name := uniqueName(t, "built-run")
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        tag,
			Cmd:          []string{"/dls-helper"},
			AttachStdout: true,
			AttachStderr: true,
		},
		HostConfig:       &container.HostConfig{NetworkMode: "none"},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		t.Fatalf("ContainerCreate from built image: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	daemon.cleanups.add("remove built container "+name, func(ctx context.Context) error {
		if _, err := api.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove container %s: %w", name, err)
		}
		return nil
	})
	if _, err := api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v\n--- daemon logs ---\n%s", created.ID, err, daemon.Logs())
	}
	waitForContainerState(t, api, created.ID, false)
	stdout, stderr := readContainerLogs(t, api, created.ID, "dockerdless-build-fixture")
	if !strings.Contains(stdout, "dockerdless-build-fixture") {
		t.Fatalf("built image logs = %q (stderr %q), want the marker file contents", stdout, stderr)
	}
	t.Logf("built image ran: id=%s stdout=%q", inspect.ID, strings.TrimSpace(stdout))
}
