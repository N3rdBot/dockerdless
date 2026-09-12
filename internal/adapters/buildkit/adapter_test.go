package buildkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func testRecord(name, configDigest string) ImageRecord {
	return ImageRecord{
		Name:         name,
		TargetDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		ConfigDigest: configDigest,
		RepoTags:     []string{name},
		RepoDigests:  []string{name + "@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
		Platform:     ocispec.Platform{OS: "linux", Architecture: "amd64"},
		Size:         4096,
		CreatedAt:    time.Unix(1700000000, 0),
		Labels:       map[string]string{"org.example": "true"},
	}
}

func TestPullReturnsConfigDigestAndNormalizesReference(t *testing.T) {
	record := testRecord("docker.io/library/alpine:latest", "sha256:aaaa")
	store := newFakeImageStore(record)
	adapter := New(store, nil, Options{Logger: zap.NewNop()})

	id, err := adapter.Pull(context.Background(), "alpine")
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}
	if id != domain.ImageID("sha256:aaaa") {
		t.Fatalf("Pull() = %q, want sha256:aaaa", id)
	}
	if len(store.pulls) != 1 || store.pulls[0] != "docker.io/library/alpine:latest" {
		t.Fatalf("store pulls = %v, want normalized reference", store.pulls)
	}
}

func TestPullWithoutStoreFails(t *testing.T) {
	adapter := New(nil, nil, Options{})
	if _, err := adapter.Pull(context.Background(), "alpine"); !errors.Is(err, ErrNoStore) {
		t.Fatalf("Pull() error = %v, want ErrNoStore", err)
	}
}

func TestPullPropagatesNotFound(t *testing.T) {
	adapter := New(newFakeImageStore(), nil, Options{})
	_, err := adapter.Pull(context.Background(), "missing:latest")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Pull() error = %v, want ErrNotFound", err)
	}
}

func TestPullImagePassesAuthAndPlatform(t *testing.T) {
	record := testRecord("registry.example.com/private:latest", "sha256:bbbb")
	store := newFakeImageStore(record)
	adapter := New(store, nil, Options{})

	auth := &RegistryAuth{Username: "user", Password: "hunter2-secret", ServerAddress: "registry.example.com"}
	if _, err := adapter.PullImage(context.Background(), "registry.example.com/private:latest", PullOptions{
		Platform: "linux/arm64",
		Auth:     auth,
	}); err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}
}

func TestPullLogsNoCredentials(t *testing.T) {
	record := testRecord("docker.io/library/private:latest", "sha256:cccc")
	store := newFakeImageStore(record)

	core, observed := observer.New(zapcore.InfoLevel)
	adapter := New(store, nil, Options{Logger: zap.New(core)})

	auth := &RegistryAuth{Username: "user", Password: "hunter2-secret", IdentityToken: "refresh-secret"}
	if _, err := adapter.PullImage(context.Background(), "private:latest", PullOptions{Auth: auth}); err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}

	entries := observed.All()
	if len(entries) == 0 {
		t.Fatal("no log entries observed")
	}
	for _, entry := range entries {
		raw, err := json.Marshal(entry.ContextMap())
		if err != nil {
			t.Fatalf("marshal log context: %v", err)
		}
		for _, secret := range []string{"hunter2-secret", "refresh-secret"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("log entry %s leaks secret %q", raw, secret)
			}
		}
	}
}

func TestInspectMapsRecord(t *testing.T) {
	record := testRecord("docker.io/library/app:v1", "sha256:dddd")
	store := newFakeImageStore(record)
	adapter := New(store, nil, Options{})

	detail, err := adapter.Inspect(context.Background(), "app:v1")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if detail.ID != domain.ImageID("sha256:dddd") {
		t.Fatalf("ID = %q", detail.ID)
	}
	if detail.Platform != "linux/amd64" {
		t.Fatalf("Platform = %q", detail.Platform)
	}
	if detail.Size != 4096 {
		t.Fatalf("Size = %d", detail.Size)
	}
	if detail.Created.IsZero() {
		t.Fatal("Created is zero")
	}
	if len(detail.RepoTags) == 0 || len(detail.RepoDigests) == 0 {
		t.Fatalf("references = %v / %v", detail.RepoTags, detail.RepoDigests)
	}
	if detail.Labels["org.example"] != "true" {
		t.Fatalf("Labels = %v", detail.Labels)
	}
}

func TestInspectMissingReturnsNotFound(t *testing.T) {
	adapter := New(newFakeImageStore(), nil, Options{})
	_, err := adapter.Inspect(context.Background(), "missing:latest")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Inspect() error = %v, want ErrNotFound", err)
	}
}

func TestListGroupsRecordsByImageID(t *testing.T) {
	latest := testRecord("docker.io/library/app:latest", "sha256:aaaa")
	previous := testRecord("docker.io/library/app:v1", "sha256:aaaa")
	other := testRecord("docker.io/library/other:latest", "sha256:bbbb")
	store := newFakeImageStore(latest, previous, other)
	adapter := New(store, nil, Options{})

	details, err := adapter.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(details) != 2 {
		t.Fatalf("List() returned %d details, want 2: %+v", len(details), details)
	}

	var appDetail *ImageDetail
	for i := range details {
		if details[i].ID == domain.ImageID("sha256:aaaa") {
			appDetail = &details[i]
		}
	}
	if appDetail == nil {
		t.Fatalf("app image missing from %+v", details)
	}
	tags := strings.Join(appDetail.RepoTags, ",")
	if !strings.Contains(tags, "app:latest") || !strings.Contains(tags, "app:v1") {
		t.Fatalf("RepoTags = %v, want both app tags", appDetail.RepoTags)
	}
}

func TestRemoveByReferenceDeletesThatReference(t *testing.T) {
	store := newFakeImageStore(testRecord("docker.io/library/app:v1", "sha256:aaaa"))
	adapter := New(store, nil, Options{})

	if err := adapter.Remove(context.Background(), domain.ImageID("docker.io/library/app:v1")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(store.deletes) != 1 || store.deletes[0] != "docker.io/library/app:v1" {
		t.Fatalf("deletes = %v", store.deletes)
	}
}

func TestRemoveMissingReferenceReturnsNotFound(t *testing.T) {
	adapter := New(newFakeImageStore(), nil, Options{})
	err := adapter.Remove(context.Background(), domain.ImageID("docker.io/library/missing:v1"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove() error = %v, want ErrNotFound", err)
	}
}

func TestRemoveByImageIDReferenceCountsOneTagPerCall(t *testing.T) {
	latest := testRecord("docker.io/library/app:latest", "sha256:aaaa")
	previous := testRecord("docker.io/library/app:v1", "sha256:aaaa")
	store := newFakeImageStore(latest, previous)
	adapter := New(store, nil, Options{})

	id := domain.ImageID("sha256:aaaa")
	if err := adapter.Remove(context.Background(), id); err != nil {
		t.Fatalf("first Remove() error = %v", err)
	}
	if len(store.deletes) != 1 || store.deletes[0] != "docker.io/library/app:latest" {
		t.Fatalf("first delete = %v, want app:latest", store.deletes)
	}
	if err := adapter.Remove(context.Background(), id); err != nil {
		t.Fatalf("second Remove() error = %v", err)
	}
	if len(store.deletes) != 2 || store.deletes[1] != "docker.io/library/app:v1" {
		t.Fatalf("second delete = %v, want app:v1", store.deletes)
	}
	if err := adapter.Remove(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("third Remove() error = %v, want ErrNotFound", err)
	}
}

func TestRemoveByImageIDPrefix(t *testing.T) {
	record := testRecord("docker.io/library/app:latest", "sha256:abcdef0123456789")
	store := newFakeImageStore(record)
	adapter := New(store, nil, Options{})

	if err := adapter.Remove(context.Background(), domain.ImageID("abcdef012345")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(store.deletes) != 1 {
		t.Fatalf("deletes = %v, want one delete", store.deletes)
	}
}

func TestRemoveEmptyIdentityFails(t *testing.T) {
	adapter := New(newFakeImageStore(), nil, Options{})
	if err := adapter.Remove(context.Background(), domain.ImageID("  ")); !errors.Is(err, domain.ErrEmptyIdentity) {
		t.Fatalf("Remove() error = %v, want ErrEmptyIdentity", err)
	}
}

func TestRemoveWithoutStoreFails(t *testing.T) {
	adapter := New(nil, nil, Options{})
	if err := adapter.Remove(context.Background(), domain.ImageID("sha256:aaaa")); !errors.Is(err, ErrNoStore) {
		t.Fatalf("Remove() error = %v, want ErrNoStore", err)
	}
}

func TestNormalizeReference(t *testing.T) {
	tests := map[string]string{
		"alpine":                      "docker.io/library/alpine:latest",
		"alpine:3.20":                 "docker.io/library/alpine:3.20",
		"library/alpine":              "docker.io/library/alpine:latest",
		"ghcr.io/owner/image":         "ghcr.io/owner/image:latest",
		"registry:5000/team/app:1.0":  "registry:5000/team/app:1.0",
		"docker.io/library/alpine:v2": "docker.io/library/alpine:v2",
	}
	for input, want := range tests {
		got, err := NormalizeReference(input)
		if err != nil {
			t.Fatalf("NormalizeReference(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeReference(%q) = %q, want %q", input, got, want)
		}
	}

	if _, err := NormalizeReference("UPPERCASE"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("NormalizeReference(UPPERCASE) error = %v, want ErrInvalidReference", err)
	}
	if _, err := NormalizeReference(""); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("NormalizeReference(\"\") error = %v, want ErrInvalidReference", err)
	}
}
