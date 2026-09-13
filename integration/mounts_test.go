//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// TestStructuredHostConfigMountAppearsInInspect proves the structured
// HostConfig.Mounts bind path end to end: a create that only uses the modern
// mount API is accepted, and the translated mount is reported by inspect. This
// is the live verification behind the compatibility doc's mount row.
func TestStructuredHostConfigMountAppearsInInspect(t *testing.T) {
	daemon := startReadyDaemon(t)
	reference := ensureFixtureImage(t, daemon, daemon.cleanups)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api := daemon.Client()

	sourceDir := t.TempDir()
	const destination = "/mnt/data"
	name := uniqueName(t, "structured-mount")

	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: reference,
			Cmd:   []string{"sh", "-c", "sleep 120"},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   sourceDir,
				Target:   destination,
				ReadOnly: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("ContainerCreate(structured bind mount): %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if created.ID == "" {
		t.Fatal("ContainerCreate returned an empty container ID")
	}
	daemon.cleanups.add("remove structured-mount container "+name, func(cleanupCtx context.Context) error {
		_, removeErr := api.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true})
		return removeErr
	})

	inspect, err := api.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect(%s): %v", created.ID, err)
	}
	for _, got := range inspect.Container.Mounts {
		if got.Type != mount.TypeBind || got.Source != sourceDir || got.Destination != destination {
			continue
		}
		if got.RW {
			t.Fatalf("structured bind mount %+v is read-write, want read-only", got)
		}
		t.Logf("structured bind mount inspect: type=%s source=%s destination=%s rw=%t",
			got.Type, got.Source, got.Destination, got.RW)
		return
	}
	t.Fatalf("inspect Mounts = %+v, want bind source=%s destination=%s", inspect.Container.Mounts, sourceDir, destination)
}
