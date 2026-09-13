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

// fixtureContextDir stages the committed fixture files plus the statically
// linked dls-helper binary into a temp directory and returns the context
// directory. The scratch-based Dockerfile therefore never needs registry
// access, whether it is built through POST /build or through testcontainers'
// FromDockerfile.
func fixtureContextDir(ctx context.Context, t *testing.T) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	fixturesDir := filepath.Join(root, "integration", "fixtures")

	contextDir := t.TempDir()
	fixturesRoot, err := os.OpenRoot(fixturesDir)
	if err != nil {
		t.Fatalf("open fixtures root %s: %v", fixturesDir, err)
	}
	defer func() { _ = fixturesRoot.Close() }()
	for _, name := range []string{"Dockerfile", "marker.txt"} {
		content, err := readWithinRoot(fixturesRoot, name)
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(contextDir, name), content, 0o600); err != nil {
			t.Fatalf("stage fixture %s: %v", name, err)
		}
	}

	helperPath := filepath.Join(contextDir, "dls-helper")
	build := exec.CommandContext(ctx, "go", "build", "-o", helperPath, "./integration/fixtures/helper")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dls-helper: %v\n%s", err, output)
	}
	return contextDir
}

// fixtureContextTar assembles the POST /build context from the staged fixture
// directory and returns the tar stream plus the fixtures source dir.
func fixtureContextTar(ctx context.Context, t *testing.T) (io.Reader, string) {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	fixturesDir := filepath.Join(root, "integration", "fixtures")
	contextDir := fixtureContextDir(ctx, t)
	contextRoot, err := os.OpenRoot(contextDir)
	if err != nil {
		t.Fatalf("open fixture context root %s: %v", contextDir, err)
	}
	defer func() { _ = contextRoot.Close() }()

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
		content, err := readWithinRoot(contextRoot, relative)
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

// readWithinRoot reads name relative to an open root. The root-scoped API
// refuses symlinks that escape the context directory, closing the TOCTOU window
// a plain os.ReadFile(path) leaves open inside a filepath.Walk callback (G122).
func readWithinRoot(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}
