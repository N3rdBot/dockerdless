package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// TestImageInspect_attachesResolvedImageConfig proves the application layer
// reads the OCI image configuration for the resolved config digest and merges
// it into the inspect projection.
func TestImageInspect_attachesResolvedImageConfig(t *testing.T) {
	// Given
	images := &fakeImages{inspectDetail: ports.ImageDetail{
		ID:       "sha256:abc123",
		RepoTags: []string{"dls-compat:latest"},
	}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})
	configs := &fakeImageConfigs{config: ports.ImageConfig{
		ExposedPorts: []string{"8080/tcp"},
		Env:          []string{"DLS_COMPAT_ENV=present"},
		Cmd:          []string{"serve", "8080"},
	}}
	service.imageConfigs = configs

	// When
	detail, err := service.ImageInspect(context.Background(), "dls-compat:latest")

	// Then
	if err != nil {
		t.Fatalf("ImageInspect: %v", err)
	}
	if detail.Config == nil {
		t.Fatal("ImageDetail.Config is nil, want the resolved OCI image configuration")
	}
	if len(detail.Config.ExposedPorts) != 1 || detail.Config.ExposedPorts[0] != "8080/tcp" {
		t.Fatalf("Config.ExposedPorts = %v, want [8080/tcp]", detail.Config.ExposedPorts)
	}
	if configs.gotConfigDigest != "sha256:abc123" {
		t.Fatalf("reader called with %q, want the resolved config digest sha256:abc123", configs.gotConfigDigest)
	}
}

// TestImageInspect_configReaderFailureKeepsInspectResult proves a config read
// failure degrades to an inspect result without Config instead of failing the
// whole request; the HTTP layer still emits a non-nil Config envelope.
func TestImageInspect_configReaderFailureKeepsInspectResult(t *testing.T) {
	// Given
	images := &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:abc123", RepoTags: []string{"dls-compat:latest"}}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})
	service.imageConfigs = &fakeImageConfigs{err: errors.New("containerd unavailable")}

	// When
	detail, err := service.ImageInspect(context.Background(), "dls-compat:latest")

	// Then
	if err != nil {
		t.Fatalf("ImageInspect: %v, want the inspect result even when the config read fails", err)
	}
	if detail.ID != "sha256:abc123" {
		t.Fatalf("detail.ID = %q, want sha256:abc123", detail.ID)
	}
	if detail.Config != nil {
		t.Fatalf("detail.Config = %+v, want nil when the reader fails", detail.Config)
	}
}

// TestImageInspect_withoutConfigReaderStillReturnsDetail proves the reader is
// optional (unit configurations and environments without a containerd socket).
func TestImageInspect_withoutConfigReaderStillReturnsDetail(t *testing.T) {
	images := &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:abc123", RepoTags: []string{"alpine:3.20"}}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})

	detail, err := service.ImageInspect(context.Background(), "alpine:3.20")
	if err != nil {
		t.Fatalf("ImageInspect: %v", err)
	}
	if detail.Config != nil {
		t.Fatalf("detail.Config = %+v, want nil without a configured reader", detail.Config)
	}
}

// TestImageRemove_missingImageReturnsDocker404 covers the not-found path.
func TestImageRemove_missingImageReturnsDocker404(t *testing.T) {
	images := &fakeImages{inspectErr: fmt.Errorf("%w: gone:latest", buildkit.ErrNotFound)}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})

	_, err := service.ImageRemove(context.Background(), "gone:latest", false)

	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ImageRemove error = %v, want ErrNotFound", err)
	}
	if got := dockerMessage(err); got != "No such image: gone:latest" {
		t.Fatalf("ImageRemove message = %q, want %q", got, "No such image: gone:latest")
	}
	if len(images.removed) != 0 {
		t.Fatalf("adapter Remove called %v times, want zero", len(images.removed))
	}
}

// TestImageRemove_runningContainerConflictsEvenWhenForced matches Docker:
// a running container's image can never be deleted, not even with force.
func TestImageRemove_runningContainerConflictsEvenWhenForced(t *testing.T) {
	images := &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:abc123", RepoTags: []string{"alpine:3.20"}}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})
	seedImageContainer(t, service, "c1", "web", domain.ContainerStateRunning, "sha256:abc123", "docker.io/library/alpine:3.20")

	for _, force := range []bool{false, true} {
		_, err := service.ImageRemove(context.Background(), "alpine:3.20", force)
		if !errors.Is(err, ports.ErrConflict) {
			t.Fatalf("force=%t: ImageRemove error = %v, want ErrConflict", force, err)
		}
		if force && !strings.Contains(dockerMessage(err), "cannot be forced") {
			t.Fatalf("force=true message = %q, want cannot be forced", dockerMessage(err))
		}
	}
	if len(images.removed) != 0 {
		t.Fatalf("adapter Remove called %v times, want zero for a running container", len(images.removed))
	}
}

// TestImageRemove_stoppedContainerRequiresForce matches Docker: a stopped
// container's image is only removable with force.
func TestImageRemove_stoppedContainerRequiresForce(t *testing.T) {
	images := &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:abc123", RepoTags: []string{"alpine:3.20"}}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})
	seedImageContainer(t, service, "c1", "web", domain.ContainerStateExited, "sha256:abc123", "docker.io/library/alpine:3.20")

	if _, err := service.ImageRemove(context.Background(), "alpine:3.20", false); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("force=false error = %v, want ErrConflict", err)
	}
	if _, err := service.ImageRemove(context.Background(), "alpine:3.20", true); err != nil {
		t.Fatalf("force=true error = %v, want success", err)
	}
	if len(images.removed) != 1 || string(images.removed[0]) != "alpine:3.20" {
		t.Fatalf("adapter Remove calls = %v, want one alpine:3.20 removal", images.removed)
	}
}

// TestImageRemove_unusedImageUntagsReference proves the success projection for
// a tag (Untagged) plus the request forwarding.
func TestImageRemove_unusedImageUntagsReference(t *testing.T) {
	images := &fakeImages{inspectDetail: ports.ImageDetail{ID: "sha256:abc123", RepoTags: []string{"alpine:3.20"}}}
	service := testService(t, newFakeRuntime(), images, newFakeNetworks(), &fakeTasks{})

	result, err := service.ImageRemove(context.Background(), "alpine:3.20", false)
	if err != nil {
		t.Fatalf("ImageRemove: %v", err)
	}
	if result.Untagged != "alpine:3.20" {
		t.Fatalf("result.Untagged = %q, want alpine:3.20", result.Untagged)
	}
	if result.Deleted != "" {
		t.Fatalf("result.Deleted = %q, want empty for a tag removal", result.Deleted)
	}
	if result.ID != "sha256:abc123" {
		t.Fatalf("result.ID = %q, want sha256:abc123", result.ID)
	}
	if len(images.removed) != 1 || string(images.removed[0]) != "alpine:3.20" {
		t.Fatalf("adapter Remove calls = %v, want one alpine:3.20 removal", images.removed)
	}
}

// seedImageContainer stores a container referencing one image so removal
// semantics can be exercised without a runtime.
func seedImageContainer(t *testing.T, service *Service, id domain.ContainerID, name string, state domain.ContainerState, digest, reference string) {
	t.Helper()
	container := domain.Container{
		ID:             id,
		Name:           name,
		Spec:           domain.ContainerSpec{Image: domain.ImageID(reference)},
		ImageReference: domain.ImageID(reference),
		ImageDigest:    domain.ImageID(digest),
		State:          state,
		CreatedAt:      time.Now().Add(-time.Minute),
	}
	if err := service.registry.Save(context.Background(), container); err != nil {
		t.Fatalf("seed image container: %v", err)
	}
}
