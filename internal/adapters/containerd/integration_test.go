//go:build integration

package containerd

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

const (
	integrationNamespace   = "dockerdless-adapter-test"
	integrationAddressEnv  = "CONTAINERD_ADDRESS"
	integrationImageEnv    = "DOCKERDLESS_TEST_IMAGE"
	integrationAddress     = "/run/containerd/containerd.sock"
	integrationStateWait   = 15 * time.Second
	integrationCleanupWait = 2 * time.Minute
)

// TestIntegrationContainerLifecycle runs the full adapter lifecycle against a
// live containerd socket in the dedicated dockerdless-adapter-test namespace.
// It skips with a recorded reason when no local image is available, and it
// removes every container, task, snapshot, image, and the namespace itself on
// both success and failure.
func TestIntegrationContainerLifecycle(t *testing.T) {
	address := os.Getenv(integrationAddressEnv)
	if address == "" {
		address = integrationAddress
	}
	client, err := containerdclient.New(address, containerdclient.WithDefaultNamespace(integrationNamespace))
	if err != nil {
		t.Skipf("integration: containerd unreachable at %s: %v", address, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx := namespaces.WithNamespace(context.Background(), integrationNamespace)
	adapter := New(client, WithNamespace(integrationNamespace))
	created := make([]domain.ContainerID, 0, 2)
	t.Cleanup(func() { cleanupIntegration(t, client, adapter, created) })

	imageRef := integrationImage(ctx, t, client)

	shortID, err := adapter.CreateContainer(ctx, Config{
		Name:       "dockerdless-it-short",
		Image:      domain.ImageID(imageRef),
		Entrypoint: []string{"/bin/sh"},
		Command:    []string{"-c", "exit 0"},
	})
	if err != nil {
		t.Fatalf("integration: create short container: %v", err)
	}
	created = append(created, shortID)
	if _, err := adapter.CreateContainer(ctx, Config{
		Name:  "dockerdless-it-short",
		Image: domain.ImageID(imageRef),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("integration: duplicate name error = %v, want ErrConflict", err)
	}
	if err := adapter.Start(ctx, shortID); err != nil {
		t.Fatalf("integration: start short container: %v", err)
	}
	wait, err := adapter.Wait(ctx, shortID)
	if err != nil {
		t.Fatalf("integration: wait short container: %v", err)
	}
	if wait.ExitCode != 0 {
		t.Fatalf("integration: short container exit code = %d, want 0", wait.ExitCode)
	}
	if state := integrationState(t, adapter, shortID); state != domain.ContainerStateExited {
		t.Fatalf("integration: short container state = %q, want exited", state)
	}
	if err := adapter.Remove(ctx, shortID); err != nil {
		t.Fatalf("integration: remove short container: %v", err)
	}

	longID, err := adapter.CreateContainer(ctx, Config{
		Name:       "dockerdless-it-long",
		Image:      domain.ImageID(imageRef),
		Entrypoint: []string{"/bin/sh"},
		Command:    []string{"-c", "trap '' TERM; sleep 30"},
	})
	if err != nil {
		t.Fatalf("integration: create long container: %v", err)
	}
	created = append(created, longID)
	if err := adapter.Start(ctx, longID); err != nil {
		t.Fatalf("integration: start long container: %v", err)
	}
	integrationWaitForState(t, adapter, longID, domain.ContainerStateRunning)

	execResult, err := adapter.Exec(ctx, longID, ExecConfig{Command: []string{"/bin/sh", "-c", "exit 7"}})
	if err != nil {
		t.Fatalf("integration: exec: %v", err)
	}
	if execResult.ExitCode == nil || *execResult.ExitCode != 7 {
		t.Fatalf("integration: exec exit code = %v, want 7", execResult.ExitCode)
	}
	record, ok := adapter.ExecRecord(execResult.ID)
	if !ok || record.ExitCode == nil || *record.ExitCode != 7 {
		t.Fatalf("integration: recorded exec = %+v, want exit code 7", record)
	}

	stopStarted := time.Now()
	if err := adapter.Stop(ctx, longID, time.Second); err != nil {
		t.Fatalf("integration: stop long container: %v", err)
	}
	if elapsed := time.Since(stopStarted); elapsed < 900*time.Millisecond {
		t.Fatalf("integration: stop escalated after %v, before the timeout", elapsed)
	}
	integrationWaitForState(t, adapter, longID, domain.ContainerStateExited)
	if err := adapter.Remove(ctx, longID); err != nil {
		t.Fatalf("integration: remove long container: %v", err)
	}
}

func integrationImage(ctx context.Context, t *testing.T, client *containerdclient.Client) string {
	t.Helper()
	if ref := os.Getenv(integrationImageEnv); ref != "" {
		if _, err := client.GetImage(ctx, ref); err != nil {
			t.Skipf("integration: %s=%q is not available in namespace %s: %v", integrationImageEnv, ref, integrationNamespace, err)
		}
		return ref
	}
	images, err := client.ImageService().List(ctx)
	if err != nil {
		t.Skipf("integration: list images in namespace %s: %v", integrationNamespace, err)
	}
	for _, image := range images {
		if image.Name != "" {
			t.Logf("integration: using local image %s", image.Name)
			return image.Name
		}
	}
	t.Skipf("integration: no local image in namespace %s; seed one offline with `ctr -n default images export --platform linux/amd64 /tmp/dockerdless-it.tar <ref>` and `ctr -n %s images import --no-unpack /tmp/dockerdless-it.tar`", integrationNamespace, integrationNamespace)
	return ""
}

func integrationState(t *testing.T, adapter *Adapter, id domain.ContainerID) domain.ContainerState {
	t.Helper()
	state, err := adapter.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("integration: status %s: %v", id, err)
	}
	return state
}

func integrationWaitForState(t *testing.T, adapter *Adapter, id domain.ContainerID, want domain.ContainerState) {
	t.Helper()
	deadline := time.Now().Add(integrationStateWait)
	last := domain.ContainerStateUnknown
	for time.Now().Before(deadline) {
		last = integrationState(t, adapter, id)
		if last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("integration: container %s state = %q, want %q", id, last, want)
}

// cleanupIntegration removes every resource the test could have created and
// finally deletes the namespace, satisfying the no-leak requirement even on
// test failure.
func cleanupIntegration(t *testing.T, client *containerdclient.Client, adapter *Adapter, created []domain.ContainerID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationCleanupWait)
	defer cancel()
	ctx = namespaces.WithNamespace(ctx, integrationNamespace)

	for _, id := range created {
		if err := adapter.Remove(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("integration cleanup: remove container %s: %v", id, err)
		}
	}
	containers, err := client.Containers(ctx)
	if err != nil {
		t.Errorf("integration cleanup: list containers: %v", err)
	}
	for _, container := range containers {
		integrationForceRemoveContainer(ctx, t, client, container)
	}
	images, err := client.ImageService().List(ctx)
	if err != nil && !errdefs.IsNotFound(err) {
		t.Errorf("integration cleanup: list images: %v", err)
	}
	for _, image := range images {
		if err := client.ImageService().Delete(ctx, image.Name); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("integration cleanup: delete image %s: %v", image.Name, err)
		}
	}
	// Containerd's garbage collector removes the deleted image's content blobs
	// and unpacked layer snapshots asynchronously, so the namespace can only be
	// deleted once it has settled.
	deadline := time.Now().Add(integrationCleanupWait)
	for {
		err := client.NamespaceService().Delete(ctx, integrationNamespace)
		if err == nil || errdefs.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("integration cleanup: delete namespace %s: %v", integrationNamespace, err)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func integrationForceRemoveContainer(ctx context.Context, t *testing.T, client *containerdclient.Client, container containerdclient.Container) {
	t.Helper()
	if task, err := container.Task(ctx, nil); err == nil {
		if status, err := task.Status(ctx); err == nil && status.Status == containerdclient.Running {
			_ = task.Kill(ctx, syscall.SIGKILL)
		}
		deadline := time.Now().Add(integrationStateWait)
		for {
			if _, err := task.Delete(ctx); err == nil || errdefs.IsNotFound(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("integration cleanup: delete task for %s did not settle", container.ID())
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	info, err := container.Info(ctx)
	if err != nil {
		t.Errorf("integration cleanup: inspect container %s: %v", container.ID(), err)
		return
	}
	if err := container.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		t.Errorf("integration cleanup: delete container %s: %v", container.ID(), err)
	}
	if info.SnapshotKey != "" {
		if err := client.SnapshotService(info.Snapshotter).Remove(ctx, info.SnapshotKey); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("integration cleanup: remove snapshot %s: %v", info.SnapshotKey, err)
		}
	}
}
