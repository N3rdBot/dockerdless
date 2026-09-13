//go:build integration

// Package integration contains environment-backed end-to-end tests for the
// dockerdless daemon. Every test drives the REAL daemon binary over a Unix
// socket against the live containerd, BuildKit, and CNI installations on the
// host. Tests skip with an explicit reason when a prerequisite is missing and
// never weaken an assertion to go green.
//
// The package is gated behind the `integration` build tag so `go test ./...`
// stays fast and hermetic.
package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var uniqueCounter atomic.Int64

const (
	defaultContainerdSocket = "/run/containerd/containerd.sock"
	defaultBuildkitSocket   = "/run/buildkit/buildkitd.sock"
	defaultCNIPluginDir     = "/opt/cni/bin"
	defaultNamespace        = "default"

	dialProbeTimeout = 2 * time.Second

	// Environment overrides keep the tests runnable against non-default
	// installations; they mirror the daemon's own configuration names.
	envContainerdSocket    = "DOCKERDLESS_CONTAINERD_SOCKET"
	envBuildkitSocket      = "DOCKERDLESS_BUILDKIT_SOCKET"
	envContainerdNamespace = "DOCKERDLESS_CONTAINERD_NAMESPACE"
	envCNIPluginDir        = "DOCKERDLESS_CNI_PLUGIN_DIR"
	envFixtureImage        = "DOCKERDLESS_TEST_IMAGE"
)

// requiredCNIPlugins are the plugins the daemon's bridge conflist invokes.
var requiredCNIPlugins = []string{"bridge", "host-local", "portmap", "firewall", "loopback"}

// prerequisites records the environment a test needs before it can run. The
// missing list holds one specific, actionable reason per unavailable
// prerequisite so a skip always says exactly what is wrong.
type prerequisites struct {
	euid             int
	containerdSocket string
	buildkitSocket   string
	cniPluginDir     string
	namespace        string
	missing          []string
}

// detectPrerequisites inspects the host without touching any daemon.
func detectPrerequisites() prerequisites {
	detected := prerequisites{
		euid:             os.Geteuid(),
		containerdSocket: envOr(envContainerdSocket, defaultContainerdSocket),
		buildkitSocket:   envOr(envBuildkitSocket, defaultBuildkitSocket),
		cniPluginDir:     envOr(envCNIPluginDir, defaultCNIPluginDir),
		namespace:        envOr(envContainerdNamespace, defaultNamespace),
	}
	if detected.euid != 0 {
		detected.missing = append(detected.missing,
			fmt.Sprintf("root (euid 0) required for containerd/BuildKit/CNI, running as euid %d", detected.euid))
	}
	detected.missing = append(detected.missing, probeUnixSocket("containerd", detected.containerdSocket)...)
	detected.missing = append(detected.missing, probeUnixSocket("BuildKit", detected.buildkitSocket)...)
	detected.missing = append(detected.missing, probeCNIPlugins(detected.cniPluginDir)...)
	return detected
}

// require skips the test with every missing prerequisite spelled out.
func (p prerequisites) require(t *testing.T) {
	t.Helper()
	if len(p.missing) > 0 {
		t.Skipf("integration prerequisites missing: %s", strings.Join(p.missing, "; "))
	}
}

func probeUnixSocket(name, path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return []string{fmt.Sprintf("%s socket %s unavailable: %v", name, path, err)}
	}
	if info.Mode()&os.ModeSocket == 0 {
		return []string{fmt.Sprintf("%s path %s is not a Unix socket (mode %s)", name, path, info.Mode())}
	}
	dialer := &net.Dialer{Timeout: dialProbeTimeout}
	conn, err := dialer.DialContext(context.Background(), "unix", path)
	if err != nil {
		return []string{fmt.Sprintf("%s socket %s is not dialable: %v", name, path, err)}
	}
	_ = conn.Close()
	return nil
}

func probeCNIPlugins(dir string) []string {
	var missing []string
	for _, plugin := range requiredCNIPlugins {
		path := filepath.Join(dir, plugin)
		info, err := os.Stat(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("CNI plugin %s unavailable: %v", path, err))
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			missing = append(missing, fmt.Sprintf("CNI plugin %s is not executable (mode %s)", path, info.Mode()))
		}
	}
	return missing
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// uniqueName builds a DNS-safe, run-unique resource name carrying the shared
// name prefix so the containerd sweep can find it even when the test never
// reached its own cleanup registration.
func uniqueName(t *testing.T, _ string) string {
	t.Helper()
	name := fmt.Sprintf("dls-%d-%d-%s", os.Getpid(), uniqueCounter.Add(1), sanitizeName(t.Name()))
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// sanitizeName lowercases and reshapes a test name so it can be used both as
// a container name and as part of a Docker image repository name.
func sanitizeName(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, character := range strings.ToLower(value) {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '.', character == '_':
			builder.WriteRune(character)
			previousDash = false
		default:
			if !previousDash {
				builder.WriteByte('-')
				previousDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}
