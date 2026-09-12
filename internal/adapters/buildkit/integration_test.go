//go:build integration

package buildkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	containerd "github.com/containerd/containerd/v2/client"
	buildkitclient "github.com/moby/buildkit/client"
	"go.uber.org/zap"
)

const (
	defaultContainerdSocket = "/run/containerd/containerd.sock"
	defaultBuildkitSocket   = "/run/buildkit/buildkitd.sock"
)

func integrationContainerdSocket() string {
	if value := os.Getenv("DOCKERDLESS_CONTAINERD_SOCKET"); value != "" {
		return value
	}
	return defaultContainerdSocket
}

func integrationBuildkitSocket() string {
	if value := os.Getenv("DOCKERDLESS_BUILDKIT_SOCKET"); value != "" {
		return value
	}
	return defaultBuildkitSocket
}

func integrationNamespace() string {
	if value := os.Getenv("DOCKERDLESS_CONTAINERD_NAMESPACE"); value != "" {
		return value
	}
	return "default"
}

func integrationSnapshotter() string {
	if value := os.Getenv("DOCKERDLESS_SNAPSHOTTER"); value != "" {
		return value
	}
	return "overlayfs"
}

func requireContainerd(t *testing.T) *containerd.Client {
	t.Helper()
	socket := integrationContainerdSocket()
	if _, err := os.Stat(socket); err != nil {
		t.Skipf("containerd socket %s is unavailable: %v", socket, err)
	}
	client, err := containerd.New(socket)
	if err != nil {
		t.Skipf("connect to containerd at %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func requireBuildkit(t *testing.T) *buildkitclient.Client {
	t.Helper()
	socket := integrationBuildkitSocket()
	if _, err := os.Stat(socket); err != nil {
		t.Skipf("BuildKit socket %s is unavailable: %v", socket, err)
	}
	client, err := buildkitclient.New(context.Background(), "unix://"+socket)
	if err != nil {
		t.Skipf("connect to BuildKit at %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// looksLikeConnectivityFailure reports whether a pull failed because the
// registry cannot be reached, so integration tests skip with a reason instead
// of reporting a false regression.
func looksLikeConnectivityFailure(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"dial tcp", "no such host", "connection refused", "i/o timeout",
		"tls handshake timeout", "context deadline exceeded", "network is unreachable",
		"connection reset by peer", "temporary failure in name resolution",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func TestIntegrationContainerdPullInspectListRemove(t *testing.T) {
	client := requireContainerd(t)
	store := NewContainerdStore(client, StoreConfig{
		Namespace:   integrationNamespace(),
		Snapshotter: integrationSnapshotter(),
	})
	adapter := New(store, nil, Options{Logger: zap.NewNop()})

	ref := "docker.io/library/hello-world:latest"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	id, err := adapter.Pull(ctx, ref)
	if err != nil {
		if looksLikeConnectivityFailure(err) {
			t.Skipf("cannot pull %s in this environment: %v", ref, err)
		}
		t.Fatalf("Pull(%s) error = %v", ref, err)
	}
	t.Logf("pulled %s -> %s", ref, id)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(context.Background()), time.Minute)
		defer cleanupCancel()
		if removeErr := adapter.Remove(cleanupCtx, id); removeErr != nil {
			t.Logf("cleanup: remove %s: %v", id, removeErr)
		}
	})

	detail, err := adapter.Inspect(ctx, ref)
	if err != nil {
		t.Fatalf("Inspect(%s) error = %v", ref, err)
	}
	if detail.ID != id {
		t.Fatalf("Inspect(%s).ID = %s, want %s", ref, detail.ID, id)
	}
	wantPlatform := runtime.GOOS + "/" + runtime.GOARCH
	if detail.Platform != wantPlatform {
		t.Fatalf("Inspect(%s).Platform = %q, want %q", ref, detail.Platform, wantPlatform)
	}
	if detail.Size <= 0 {
		t.Fatalf("Inspect(%s).Size = %d, want > 0", ref, detail.Size)
	}
	if detail.Created.IsZero() {
		t.Fatalf("Inspect(%s).Created is zero", ref)
	}
	if len(detail.RepoDigests) == 0 {
		t.Fatalf("Inspect(%s).RepoDigests is empty", ref)
	}
	t.Logf("inspect: id=%s platform=%s size=%d tags=%v digests=%v",
		detail.ID, detail.Platform, detail.Size, detail.RepoTags, detail.RepoDigests)

	images, err := adapter.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	found := false
	for _, image := range images {
		if image.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List() did not contain image %s", id)
	}
}

func TestIntegrationBuildkitMinimalDockerfile(t *testing.T) {
	containerdClient := requireContainerd(t)
	buildkitClient := requireBuildkit(t)

	store := NewContainerdStore(containerdClient, StoreConfig{
		Namespace:   integrationNamespace(),
		Snapshotter: integrationSnapshotter(),
	})
	solver := NewBuildkitSolver(buildkitClient)
	tempRoot := t.TempDir()
	adapter := New(store, solver, Options{TempRoot: tempRoot, Logger: zap.NewNop()})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tag := fmt.Sprintf("docker.io/library/dockerdless-buildtest-%d:latest", time.Now().UnixNano())
	contextTar := buildContextTar(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY hello.txt /hello.txt\n",
		"hello.txt":  "hello from dockerdless\n",
	})

	var out bytes.Buffer
	if err := adapter.Build(ctx, BuildRequest{Context: bytes.NewReader(contextTar), Tag: tag}, &out); err != nil {
		t.Fatalf("Build() error = %v\nprogress:\n%s", err, out.String())
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	t.Logf("captured %d Docker JSON progress lines for %s", len(lines), tag)
	for _, line := range lines {
		t.Logf("progress: %s", line)
	}

	messages := decodeDockerStream(t, out.Bytes())
	var imageID string
	for _, message := range messages {
		if message.Aux != nil && message.Aux.ID != "" {
			imageID = message.Aux.ID
		}
		if message.ErrorDetail != nil {
			t.Fatalf("build stream carried errorDetail: %+v", message.ErrorDetail)
		}
	}
	if imageID == "" {
		t.Fatalf("build stream carried no aux image ID:\n%s", out.String())
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(context.Background()), time.Minute)
		defer cleanupCancel()
		if removeErr := adapter.Remove(cleanupCtx, domain.ImageID(tag)); removeErr != nil {
			t.Logf("cleanup: remove %s: %v", tag, removeErr)
		}
	})

	detail, err := adapter.Inspect(ctx, tag)
	if err != nil {
		t.Fatalf("Inspect(%s) error = %v", tag, err)
	}
	if detail.ID != domain.ImageID(imageID) {
		t.Fatalf("Inspect(%s).ID = %s, want aux ID %s", tag, detail.ID, imageID)
	}
	t.Logf("built image inspect: id=%s platform=%s size=%d tags=%v",
		detail.ID, detail.Platform, detail.Size, detail.RepoTags)

	if entries, err := os.ReadDir(tempRoot); err != nil {
		t.Fatalf("ReadDir(temp root) error = %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("temp build contexts leaked: %v", entries)
	}
}

func TestIntegrationBuildkitMalformedDockerfileReportsErrorDetail(t *testing.T) {
	containerdClient := requireContainerd(t)
	buildkitClient := requireBuildkit(t)

	store := NewContainerdStore(containerdClient, StoreConfig{
		Namespace:   integrationNamespace(),
		Snapshotter: integrationSnapshotter(),
	})
	adapter := New(store, NewBuildkitSolver(buildkitClient), Options{TempRoot: t.TempDir(), Logger: zap.NewNop()})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tag := fmt.Sprintf("docker.io/library/dockerdless-broken-%d:latest", time.Now().UnixNano())
	contextTar := buildContextTar(t, map[string]string{"Dockerfile": "FROM scratch\nTHIS IS NOT A DOCKERFILE\n"})

	var out bytes.Buffer
	err := adapter.Build(ctx, BuildRequest{Context: bytes.NewReader(contextTar), Tag: tag}, &out)
	if err == nil {
		t.Fatal("Build() error = nil, want malformed Dockerfile failure")
	}

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		t.Logf("progress: %s", line)
	}
	messages := decodeDockerStream(t, out.Bytes())
	foundError := false
	for _, message := range messages {
		if message.ErrorDetail != nil && message.ErrorDetail.Message != "" {
			foundError = true
			t.Logf("errorDetail: %s", message.ErrorDetail.Message)
		}
	}
	if !foundError {
		t.Fatalf("no errorDetail in malformed build stream:\n%s", out.String())
	}

	if entries, readErr := os.ReadDir(adapter.tempRoot); readErr != nil {
		t.Fatalf("ReadDir(temp root) error = %v", readErr)
	} else if len(entries) != 0 {
		t.Fatalf("temp build contexts leaked after failed build: %v", entries)
	}
}

func TestIntegrationPullNonexistentImageReturnsNotFound(t *testing.T) {
	client := requireContainerd(t)
	store := NewContainerdStore(client, StoreConfig{
		Namespace:   integrationNamespace(),
		Snapshotter: integrationSnapshotter(),
	})
	adapter := New(store, nil, Options{Logger: zap.NewNop()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ref := fmt.Sprintf("docker.io/library/dockerdless-does-not-exist-%d:latest", time.Now().UnixNano())
	_, err := adapter.Pull(ctx, ref)
	if err == nil {
		t.Fatal("Pull() error = nil, want not-found failure")
	}
	if looksLikeConnectivityFailure(err) {
		t.Skipf("cannot reach the registry from this environment: %v", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Pull() error = %v, want ErrNotFound", err)
	}
	t.Logf("not-found error: %v", err)
}
