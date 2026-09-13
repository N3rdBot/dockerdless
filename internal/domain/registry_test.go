package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

func TestRegistryStateInterfaceAndRename(t *testing.T) {
	ctx := context.Background()
	id, err := domain.NewContainerID("registry-container")
	if err != nil {
		t.Fatalf("construct container id: %v", err)
	}
	image, err := domain.NewImageID("registry:latest")
	if err != nil {
		t.Fatalf("construct image id: %v", err)
	}
	digest, err := domain.NewImageID("sha256:0123456789abcdef")
	if err != nil {
		t.Fatalf("construct image digest: %v", err)
	}
	container := domain.Container{
		ID:          id,
		Name:        "before-rename",
		Spec:        domain.ContainerSpec{Image: image},
		ImageDigest: digest,
		Labels:      map[string]string{"role": "api"},
		State:       domain.ContainerStateRunning,
	}
	registry := domain.NewRegistry()

	if err := registry.Save(ctx, container); err != nil {
		t.Fatalf("save container: %v", err)
	}
	if err := registry.Rename(id, "after-rename"); err != nil {
		t.Fatalf("rename container: %v", err)
	}
	if _, err := registry.GetByName("before-rename"); !errors.Is(err, domain.ErrContainerNotFound) {
		t.Fatalf("expected old name to be absent, got %v", err)
	}
	byName, err := registry.GetByName("after-rename")
	if err != nil {
		t.Fatalf("get renamed container: %v", err)
	}
	if byName.ID != id {
		t.Fatalf("expected renamed container ID %q, got %q", id, byName.ID)
	}

	byID, err := registry.Get(ctx, id)
	if err != nil {
		t.Fatalf("get by ID: %v", err)
	}
	if byID.ImageDigest != digest {
		t.Fatalf("expected pinned digest %q, got %q", digest, byID.ImageDigest)
	}
	if byID.ImageReference != image {
		t.Fatalf("expected image reference %q, got %q", image, byID.ImageReference)
	}
	byID.Labels["role"] = "mutated-copy"
	again, err := registry.Get(ctx, id)
	if err != nil {
		t.Fatalf("get defensive copy: %v", err)
	}
	if again.Labels["role"] != "api" {
		t.Fatalf("registry returned aliased labels: %#v", again.Labels)
	}

	if got := registry.List(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("expected one listed container %q, got %#v", id, got)
	}
	if err := registry.Remove(ctx, id); err != nil {
		t.Fatalf("remove container: %v", err)
	}
	if _, err := registry.Get(ctx, id); !errors.Is(err, domain.ErrContainerNotFound) {
		t.Fatalf("expected removed container to be absent, got %v", err)
	}
}

func TestRegistryRejectsCancelledStateOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry := domain.NewRegistry()
	id := domain.ContainerID("cancelled")
	if _, err := registry.Get(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled Get, got %v", err)
	}
	if err := registry.Save(ctx, domain.Container{ID: id}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled Save, got %v", err)
	}
	if err := registry.Remove(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled Remove, got %v", err)
	}
}
