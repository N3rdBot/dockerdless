//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
)

const (
	// adapterNameLabel is the containerd label the daemon's runtime adapter
	// stores the Docker container name in (internal/adapters/containerd).
	adapterNameLabel = "io.dockerdless.name"

	cleanupTimeout = 2 * time.Minute
	sweepTimeout   = 30 * time.Second
)

// cleanupRegistry is a LIFO cleanup chain captured by t.Cleanup, so it runs on
// pass, failure, and panic alike. Steps are best-effort and every error is
// surfaced through t.Errorf rather than being swallowed.
type cleanupRegistry struct {
	t     *testing.T
	mu    sync.Mutex
	steps []cleanupStep
}

type cleanupStep struct {
	name string
	run  func(context.Context) error
}

func newCleanupRegistry(t *testing.T) *cleanupRegistry {
	t.Helper()
	registry := &cleanupRegistry{t: t}
	t.Cleanup(registry.run)
	return registry
}

func (r *cleanupRegistry) add(name string, run func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, cleanupStep{name: name, run: run})
}

func (r *cleanupRegistry) run() {
	r.mu.Lock()
	steps := append([]cleanupStep(nil), r.steps...)
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), cleanupTimeout)
	defer cancel()
	for _, step := range slices.Backward(steps) {
		if err := step.run(ctx); err != nil {
			r.t.Errorf("cleanup %q: %v", step.name, err)
		}
	}
}

// removeImageFromContainerd deletes one image record directly through the
// containerd client. The daemon's HTTP surface has no DELETE /images/{name}
// route yet, so image cleanup cannot go through the API.
func removeImageFromContainerd(ctx context.Context, containerdSocket, namespace, reference string) error {
	client, err := containerd.New(containerdSocket, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return fmt.Errorf("connect containerd to remove image %s: %w", reference, err)
	}
	defer func() { _ = client.Close() }()
	ctx = namespaces.WithNamespace(ctx, namespace)

	images, err := client.ImageService().List(ctx)
	if err != nil {
		return fmt.Errorf("list images to remove %s: %w", reference, err)
	}
	var problems []error
	for _, image := range images {
		if image.Name != reference {
			continue
		}
		if deleteErr := client.ImageService().Delete(ctx, image.Name); deleteErr != nil && !errdefs.IsNotFound(deleteErr) {
			problems = append(problems, fmt.Errorf("remove image %s: %w", image.Name, deleteErr))
		}
	}
	return errors.Join(problems...)
}

// sweepAdapterState is the last-resort backstop: it deletes every containerd
// container whose Docker name carries the test run prefix, plus every image
// tagged with that prefix, even when a test failed before registering its own
// cleanup. It deliberately ignores anything it does not recognize as its own.
func sweepAdapterState(ctx context.Context, containerdSocket, namespace, namePrefix string) error {
	client, err := containerd.New(containerdSocket, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return fmt.Errorf("connect containerd for leftover sweep: %w", err)
	}
	defer func() { _ = client.Close() }()
	ctx = namespaces.WithNamespace(ctx, namespace)

	var problems []error
	containers, err := client.Containers(ctx)
	if err != nil {
		return fmt.Errorf("list containers for leftover sweep: %w", err)
	}
	for _, container := range containers {
		labels, labelErr := container.Labels(ctx)
		if labelErr != nil {
			problems = append(problems, fmt.Errorf("read labels of container %s: %w", container.ID(), labelErr))
			continue
		}
		if !strings.HasPrefix(labels[adapterNameLabel], namePrefix) {
			continue
		}
		if removeErr := forceRemoveContainer(ctx, client, container); removeErr != nil {
			problems = append(problems, fmt.Errorf("remove leftover container %s: %w", container.ID(), removeErr))
		}
	}

	images, err := client.ImageService().List(ctx)
	if err != nil {
		problems = append(problems, fmt.Errorf("list images for leftover sweep: %w", err))
	} else {
		for _, image := range images {
			if !strings.Contains(image.Name, namePrefix) {
				continue
			}
			if deleteErr := client.ImageService().Delete(ctx, image.Name); deleteErr != nil && !errdefs.IsNotFound(deleteErr) {
				problems = append(problems, fmt.Errorf("remove leftover image %s: %w", image.Name, deleteErr))
			}
		}
	}
	return errors.Join(problems...)
}

func forceRemoveContainer(ctx context.Context, client *containerd.Client, container containerd.Container) error {
	if err := killAndDeleteContainerTask(ctx, container); err != nil {
		return err
	}
	return removeContainerRecordAndSnapshot(ctx, client, container)
}

// killAndDeleteContainerTask SIGKILLs a running task and polls task.Delete
// until the task record settles or sweepTimeout expires.
func killAndDeleteContainerTask(ctx context.Context, container containerd.Container) error {
	task, err := container.Task(ctx, nil)
	if err == nil {
		if status, statusErr := task.Status(ctx); statusErr == nil && status.Status == containerd.Running {
			_ = task.Kill(ctx, syscall.SIGKILL)
		}
		deadline := time.Now().Add(sweepTimeout)
		for {
			if _, err := task.Delete(ctx); err == nil || errdefs.IsNotFound(err) {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("task %s did not settle after SIGKILL", container.ID())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil // no task recorded; nothing to kill or delete
}

// removeContainerRecordAndSnapshot deletes the container record and its
// snapshot, tolerating records a concurrent sweep already removed.
func removeContainerRecordAndSnapshot(ctx context.Context, client *containerd.Client, container containerd.Container) error {
	info, err := container.Info(ctx)
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect container %s: %w", container.ID(), err)
	}
	if err := container.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("delete container %s: %w", container.ID(), err)
	}
	if info.SnapshotKey == "" {
		return nil
	}
	if err := client.SnapshotService(info.Snapshotter).Remove(ctx, info.SnapshotKey); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove snapshot %s: %w", info.SnapshotKey, err)
	}
	return nil
}

// deleteLeftoverBridges removes every CNI bridge named by a conflist in the
// daemon's ephemeral config directory. The daemon deletes bridges on network
// removal; this is the backstop for failed runs where that removal never ran.
func deleteLeftoverBridges(ctx context.Context, cniConfigDir string) error {
	entries, err := os.ReadDir(cniConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read CNI config dir %s: %w", cniConfigDir, err)
	}

	var problems []error
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".conflist" && extension != ".conf" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(cniConfigDir, entry.Name()))
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s: %w", entry.Name(), err))
			continue
		}
		var document struct {
			Plugins []struct {
				Bridge string `json:"bridge"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			problems = append(problems, fmt.Errorf("parse %s: %w", entry.Name(), err))
			continue
		}
		for _, plugin := range document.Plugins {
			if plugin.Bridge == "" {
				continue
			}
			if _, ok := seen[plugin.Bridge]; ok {
				continue
			}
			seen[plugin.Bridge] = struct{}{}
			if err := deleteNetworkLink(ctx, plugin.Bridge); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

func deleteNetworkLink(ctx context.Context, name string) error {
	output, err := exec.CommandContext(ctx, "ip", "link", "del", name).CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.ToLower(string(output))
	if strings.Contains(text, "cannot find device") || strings.Contains(text, "does not exist") {
		return nil
	}
	return fmt.Errorf("delete bridge %s: %w (%s)", name, err, strings.TrimSpace(string(output)))
}
