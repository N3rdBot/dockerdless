//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func TestContainerLogsFollowEndsWhenContainerExits(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api := daemon.Client()
	name := uniqueName(t, "logs-follow")
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        reference,
			Cmd:          []string{"sh", "-c", "echo logs-follow-marker; sleep 1; echo logs-follow-final; exit 0"},
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
		_, removeErr := api.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
		return removeErr
	})
	if _, err := api.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v\n--- daemon logs ---\n%s", containerID, err, daemon.Logs())
	}

	stream, err := api.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		t.Fatalf("ContainerLogs(%s): %v", containerID, err)
	}
	defer stream.Close()

	result := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	started := time.Now()
	go func() {
		_, copyErr := stdcopy.StdCopy(&stdout, &stderr, stream)
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			result <- copyErr
			return
		}
		result <- nil
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("demux followed logs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("followed logs did not end after container exit (elapsed %s)", time.Since(started).Round(time.Millisecond))
	}
	if stdout.String() != "logs-follow-marker\nlogs-follow-final\n" {
		t.Fatalf("stdout = %q, want both log lines", stdout.String())
	}
	t.Logf("follow returned after %s: stdout=%q stderr=%q", time.Since(started).Round(time.Millisecond), stdout.String(), stderr.String())
}
