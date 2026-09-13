//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	sharedBuildOnce sync.Once
	sharedBuildRoot string
	sharedBuildPath string
	sharedBuildErr  error
)

// daemonBinary builds cmd/dockerdless once per test binary into a shared temp
// directory. A build failure is a real test failure: the daemon must compile.
func daemonBinary(t *testing.T) string {
	t.Helper()
	sharedBuildOnce.Do(buildDaemonBinary)
	if sharedBuildErr != nil {
		t.Fatalf("build the real dockerdless daemon: %v", sharedBuildErr)
	}
	return sharedBuildPath
}

func buildDaemonBinary() {
	root, err := moduleRoot()
	if err != nil {
		sharedBuildErr = err
		return
	}
	dir, err := os.MkdirTemp("", "dockerdless-e2e-bin-")
	if err != nil {
		sharedBuildErr = fmt.Errorf("create shared build directory: %w", err)
		return
	}
	sharedBuildRoot = dir

	binary := filepath.Join(dir, "dockerdless")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", binary, "./cmd/dockerdless")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		sharedBuildErr = fmt.Errorf("%w\n%s", err, output)
		return
	}
	sharedBuildPath = binary
}

// cleanupSharedBuild removes the shared binary directory after TestMain.
func cleanupSharedBuild() {
	if sharedBuildRoot == "" {
		return
	}
	_ = os.RemoveAll(sharedBuildRoot)
	sharedBuildRoot = ""
	sharedBuildPath = ""
}

func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot resolve the integration package source path")
	}
	root := filepath.Dir(filepath.Dir(file))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("locate module root from %s: %w", file, err)
	}
	return root, nil
}
