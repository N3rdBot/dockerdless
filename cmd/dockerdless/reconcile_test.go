package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/observability"
)

func TestRestoredContainerRetainsImageDigestAndCommand(t *testing.T) {
	createdAt := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	labels := map[string]string{
		"io.dockerdless.name":     "web",
		containerImageDigestLabel: "sha256:cafebabe",
		containerCommandLabel:     `["/bin/sh","-c","echo hello"]`,
	}

	container, err := restoredContainer("container-1", "alpine:latest", labels, createdAt, createdAt)
	if err != nil {
		t.Fatalf("restore container metadata: %v", err)
	}
	if container.ImageDigest != "sha256:cafebabe" {
		t.Fatalf("expected restored digest, got %q", container.ImageDigest)
	}
	if got := container.Spec.Command; len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" || got[2] != "echo hello" {
		t.Fatalf("expected restored command, got %#v", got)
	}
}

func TestReconcileAtStartupPopulatesRegistryFromSnapshot(t *testing.T) {
	logger, err := observability.NewLogger("info")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	registry := domain.NewRegistry()
	err = reconcileAtStartup(context.Background(), registry, logger, func(context.Context) ([]domain.Container, []domain.TaskSnapshot, error) {
		return []domain.Container{{ID: "running", Name: "running"}, {ID: "exited", Name: "exited"}}, []domain.TaskSnapshot{{ContainerID: "running", Status: domain.TaskStateRunning}, {ContainerID: "exited", Status: domain.TaskStateStopped, ExitCode: 42}}, nil
	})
	if err != nil {
		t.Fatalf("reconcile startup: %v", err)
	}
	running, err := registry.Get(context.Background(), "running")
	if err != nil || running.State != domain.ContainerStateRunning {
		t.Fatalf("running recovery: %#v, %v", running, err)
	}
	exited, err := registry.Get(context.Background(), "exited")
	if err != nil || exited.State != domain.ContainerStateExited || exited.ExitCode != 42 {
		t.Fatalf("exited recovery: %#v, %v", exited, err)
	}
}

func TestReconcileAtStartupToleratesSnapshotFailure(t *testing.T) {
	logger, err := observability.NewLogger("info")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	registry := domain.NewRegistry()
	wantErr := errors.New("containerd unavailable")
	if err := reconcileAtStartup(context.Background(), registry, logger, func(context.Context) ([]domain.Container, []domain.TaskSnapshot, error) {
		return nil, nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("expected snapshot failure, got %v", err)
	}
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("failed reconciliation populated registry: %#v", got)
	}
}
