//go:build integration

package integration

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// TestDaemonFailureSemantics hardens the Docker-shaped failures the matrix
// promises: invalid image, duplicate container name, published-port collision,
// image deletion while in use, and repeated forced cleanup.
func TestDaemonFailureSemantics(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api := daemon.Client()

	const missingImage = "docker.io/library/dockerdless-definitely-missing:latest"
	if _, err := api.ImageInspect(ctx, missingImage); !errdefs.IsNotFound(err) {
		t.Fatalf("ImageInspect(%s) error = %v, want Docker 404", missingImage, err)
	}
	_, createErr := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             uniqueName(t, "missing-image"),
		Config:           &container.Config{Image: missingImage},
		HostConfig:       &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if !errdefs.IsNotFound(createErr) {
		t.Fatalf("ContainerCreate with a missing image error = %v, want Docker 404", createErr)
	}
	if message := errorMessage(createErr); !strings.Contains(message, "No such image") {
		t.Fatalf("missing image message = %q, want Docker No such image", message)
	}

	name := uniqueName(t, "duplicate")
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig:       &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		t.Fatalf("first ContainerCreate(%s): %v\n--- daemon logs ---\n%s", name, err, daemon.Logs())
	}
	_, duplicateErr := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig:       &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if !errdefs.IsConflict(duplicateErr) {
		t.Fatalf("duplicate ContainerCreate(%s) error = %v, want Docker 409", name, duplicateErr)
	}
	if message := errorMessage(duplicateErr); !strings.Contains(message, "already in use") {
		t.Fatalf("duplicate name message = %q, want Docker name-in-use conflict", message)
	}
	removeContainer(t, daemon, created.ID, name)

	probe, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a host port: %v", err)
	}
	hostPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	portBindings := network.PortMap{
		mustPort(t, "8080/tcp"): []network.PortBinding{{HostPort: strconv.Itoa(hostPort)}},
	}
	first, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: uniqueName(t, "port-a"),
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig:       &container.HostConfig{PortBindings: portBindings},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		t.Fatalf("ContainerCreate with fixed host port %d: %v\n--- daemon logs ---\n%s", hostPort, err, daemon.Logs())
	}
	_, collisionErr := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: uniqueName(t, "port-b"),
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig:       &container.HostConfig{PortBindings: portBindings},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if !errdefs.IsConflict(collisionErr) {
		t.Fatalf("port collision error = %v, want Docker 409", collisionErr)
	}
	if message := errorMessage(collisionErr); !strings.Contains(message, "already allocated") {
		t.Fatalf("port collision message = %q, want the allocator conflict", message)
	}
	removeContainer(t, daemon, first.ID, "port-a")

	inUse, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: uniqueName(t, "in-use"),
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig:       &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		t.Fatalf("ContainerCreate for image-in-use: %v", err)
	}
	if _, err := api.ContainerStart(ctx, inUse.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v\n--- daemon logs ---\n%s", inUse.ID, err, daemon.Logs())
	}
	if _, err := api.ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: true, PruneChildren: true}); !errdefs.IsConflict(err) {
		t.Fatalf("ImageRemove while a container runs = %v, want Docker 409", err)
	}
	if _, err := api.ContainerRemove(ctx, inUse.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatalf("ContainerRemove(%s): %v", inUse.ID, err)
	}
	if _, err := api.ContainerRemove(ctx, inUse.ID, client.ContainerRemoveOptions{Force: true}); !errdefs.IsNotFound(err) {
		t.Fatalf("second ContainerRemove = %v, want Docker 404 for repeated forced cleanup", err)
	}
	if _, err := api.ImageRemove(ctx, missingImage, client.ImageRemoveOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("ImageRemove(%s) = %v, want Docker 404", missingImage, err)
	}
	t.Log("failure semantics: 404 missing image, 409 duplicate name, 409 port collision, 409 image in use, 404 repeated cleanup")
}

func removeContainer(t *testing.T, daemon *daemonProcess, id, name string) {
	t.Helper()
	if _, err := daemon.Client().ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		t.Fatalf("remove container %s (%s): %v", name, id, err)
	}
}

// TestDaemonUnsupportedEndpoints pins the exact Docker-shaped error for
// endpoints outside the MVP: every unregistered route answers the same 404
// envelope Docker uses for unknown pages, never a silent success.
func TestDaemonUnsupportedEndpoints(t *testing.T) {
	daemon := startReadyDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	api := daemon.Client()

	checks := []struct {
		name string
		call func() error
	}{
		{"GET /volumes", func() error {
			_, err := api.VolumeList(ctx, client.VolumeListOptions{})
			return err
		}},
		{"POST /auth", func() error {
			_, err := api.RegistryLogin(ctx, client.RegistryLoginOptions{Username: "u", Password: "p"})
			return err
		}},
		{"POST /images/{name}/push", func() error {
			_, err := api.ImagePush(ctx, "docker.io/library/alpine:3.20", client.ImagePushOptions{})
			return err
		}},
		{"POST /containers/{id}/kill", func() error {
			_, err := api.ContainerKill(ctx, "dls-missing", client.ContainerKillOptions{Signal: "SIGKILL"})
			return err
		}},
		{"POST /networks/{id}/disconnect", func() error {
			_, err := api.NetworkDisconnect(ctx, "bridge", client.NetworkDisconnectOptions{Container: "dls-missing", Force: true})
			return err
		}},
	}
	for _, check := range checks {
		err := check.call()
		if !errdefs.IsNotFound(err) {
			t.Fatalf("%s error = %v, want Docker 404", check.name, err)
		}
		if !strings.Contains(err.Error(), "page not found") {
			t.Fatalf("%s message = %q, want Docker page not found", check.name, err.Error())
		}
		t.Logf("%s -> 404 %q", check.name, err.Error())
	}
}

// TestDaemonMultiNetworkAttachRequiresRunningContainer documents the exact
// behavior testcontainers hits for a request with more than one network: it
// connects the extra networks after create but before start, which this MVP
// rejects because the CNI attachment needs a live network namespace.
func TestDaemonMultiNetworkAttachRequiresRunningContainer(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	api := daemon.Client()

	netName := uniqueName(t, "attach-net")
	createdNetwork, err := api.NetworkCreate(ctx, netName, client.NetworkCreateOptions{Driver: "bridge"})
	if err != nil {
		t.Fatalf("NetworkCreate(%s): %v\n--- daemon logs ---\n%s", netName, err, daemon.Logs())
	}
	daemon.cleanups.add("remove network "+netName, func(ctx context.Context) error {
		if _, err := api.NetworkRemove(ctx, createdNetwork.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		return nil
	})

	name := uniqueName(t, "multi-net")
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig: &container.HostConfig{},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{"bridge": {}},
		},
	})
	if err != nil {
		t.Fatalf("ContainerCreate(%s): %v\n--- daemon logs ---\n%s", name, err, daemon.Logs())
	}
	daemon.cleanups.add("remove container "+name, func(ctx context.Context) error {
		if _, err := api.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		return nil
	})

	if _, err := api.NetworkConnect(ctx, createdNetwork.ID, client.NetworkConnectOptions{
		Container:      created.ID,
		EndpointConfig: &network.EndpointSettings{},
	}); !errdefs.IsConflict(err) {
		t.Fatalf("NetworkConnect before start = %v, want Docker 409", err)
	} else if !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("NetworkConnect before start message = %q, want is not running", err.Error())
	}
	t.Log("multi-network attach before start -> 409 Container is not running (documented limitation)")

	if _, err := api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v\n--- daemon logs ---\n%s", created.ID, err, daemon.Logs())
	}
	if _, err := api.NetworkConnect(ctx, createdNetwork.ID, client.NetworkConnectOptions{
		Container:      created.ID,
		EndpointConfig: &network.EndpointSettings{},
	}); err == nil {
		t.Fatal("NetworkConnect after start unexpectedly attached a second network; the MVP supports one network per container")
	} else if !strings.Contains(err.Error(), "cni: connecting") {
		t.Fatalf("NetworkConnect after start error = %v, want the CNI attach failure", err)
	} else {
		t.Logf("multi-network attach after start -> %q (documented limitation: one network per container)", err.Error())
	}
	inspect, err := api.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	attached := inspect.Container.NetworkSettings.Networks
	if _, ok := attached["bridge"]; !ok {
		t.Fatalf("inspect networks = %v, want the bridge attachment", attached)
	}
	if len(attached) != 1 {
		t.Fatalf("inspect networks = %v, want exactly the single bridge attachment", attached)
	}
	t.Logf("second network was not attached; inspect networks=%v", mapKeys(attached))
}

func mapKeys(values map[string]*network.EndpointSettings) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func mustPort(t *testing.T, value string) network.Port {
	t.Helper()
	port, err := network.ParsePort(value)
	if err != nil {
		t.Fatalf("parse port %q: %v", value, err)
	}
	return port
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
