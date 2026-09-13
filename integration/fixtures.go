//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const (
	// defaultFixtureImage is present in the host's `default` containerd
	// namespace, so the lifecycle tests never need the network.
	defaultFixtureImage = "docker.io/library/alpine:3.20"
	fixturePullTimeout  = 3 * time.Minute
)

// ensureFixtureImage returns a local image reference, pulling it only when it
// is absent. An image this test pulled is registered for removal; a pre-existing
// image is reused and explicitly left in place.
func ensureFixtureImage(t *testing.T, daemon *daemonProcess, registry *cleanupRegistry) string {
	t.Helper()
	reference := envOr(envFixtureImage, defaultFixtureImage)
	api := daemon.Client()

	ctx, cancel := context.WithTimeout(context.Background(), fixturePullTimeout)
	defer cancel()

	if _, err := api.ImageInspect(ctx, reference); err == nil {
		t.Logf("fixture image %s already present locally (reused, not removed during cleanup)", reference)
		return reference
	}

	response, err := api.ImagePull(ctx, reference, client.ImagePullOptions{})
	if err != nil {
		if looksLikeConnectivityFailure(err) {
			t.Skipf("fixture image %s is not present locally and the registry is unreachable: %v", reference, err)
		}
		t.Fatalf("pull fixture image %s: %v\n--- daemon logs ---\n%s", reference, err, daemon.Logs())
	}
	defer func() { _ = response.Close() }()
	if err := response.Wait(ctx); err != nil {
		if looksLikeConnectivityFailure(err) {
			t.Skipf("fixture image %s is not present locally and the registry is unreachable: %v", reference, err)
		}
		t.Fatalf("pull fixture image %s: %v\n--- daemon logs ---\n%s", reference, err, daemon.Logs())
	}
	inspect, err := api.ImageInspect(ctx, reference)
	if err != nil {
		t.Fatalf("inspect pulled fixture image %s: %v", reference, err)
	}
	storedReference := reference
	if len(inspect.RepoTags) > 0 {
		storedReference = inspect.RepoTags[0]
	}

	registry.add("remove pulled fixture image "+storedReference, func(ctx context.Context) error {
		return removeImageFromContainerd(ctx, daemon.containerdSocket, daemon.namespace, storedReference)
	})
	t.Logf("pulled fixture image %s (registered for removal during cleanup)", storedReference)
	return reference
}

// looksLikeConnectivityFailure reports whether a pull failed because the
// registry cannot be reached, so tests skip with a reason instead of reporting
// a false regression.
func looksLikeConnectivityFailure(err error) bool {
	if err == nil {
		return false
	}
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

// fixtureContextTar assembles the POST /build context: the committed fixture
// files plus the statically linked dls-helper binary built from
// integration/fixtures/helper. The scratch-based Dockerfile therefore never
// needs registry access.
func fixtureContextTar(t *testing.T) (io.Reader, string) {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	fixturesDir := filepath.Join(root, "integration", "fixtures")

	contextDir := t.TempDir()
	for _, name := range []string{"Dockerfile", "marker.txt"} {
		content, err := os.ReadFile(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(contextDir, name), content, 0o644); err != nil {
			t.Fatalf("stage fixture %s: %v", name, err)
		}
	}

	helperPath := filepath.Join(contextDir, "dls-helper")
	build := exec.Command("go", "build", "-o", helperPath, "./integration/fixtures/helper")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dls-helper: %v\n%s", err, output)
	}

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err = filepath.Walk(contextDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tar fixture context %s: %v", contextDir, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close fixture context tar: %v", err)
	}
	return bytes.NewReader(buffer.Bytes()), fixturesDir
}
