//go:build integration

package integration

import (
	"net"
	"testing"
	"time"

	dockertypes "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

const (
	compatTestTimeout     = 5 * time.Minute
	compatWaitTimeout     = 90 * time.Second
	compatRegistryProbe   = 3 * time.Second
	compatRegistryAddress = "registry-1.docker.io:443"
)

// TestTestcontainersGoCompatibilityMatrix drives the REAL
// github.com/testcontainers/testcontainers-go v0.44 library against one real
// daemon and one real containerd/BuildKit/CNI installation, with Ryuk
// disabled. testcontainers caches the resolved DOCKER_HOST process-wide, so
// every consumer in this binary shares a single daemon started here.
func TestTestcontainersGoCompatibilityMatrix(t *testing.T) {
	prerequisites := detectPrerequisites()
	prerequisites.require(t)
	daemon := startDaemon(t, prerequisites)
	configureTestcontainers(t, daemon)

	t.Run("ImagePull", func(t *testing.T) { compatImagePull(t, daemon) })
	t.Run("ImageInspectConfig", func(t *testing.T) { compatImageInspectConfig(t, daemon) })
	t.Run("ContainerWithoutExposedPorts", func(t *testing.T) { compatContainerWithoutExposedPorts(t, daemon) })
	t.Run("PrivilegedCreateRejected", func(t *testing.T) { compatPrivilegedCreateRejected(t, daemon) })
	t.Run("BuildPublishedPortsAndWaitForHTTP", func(t *testing.T) { compatBuildPublishedPortsAndWaitForHTTP(t, daemon) })
	t.Run("WaitForExecAndExitCodes", func(t *testing.T) { compatWaitForExecAndExitCodes(t, daemon) })
	t.Run("NetworkCreateInspectListRemove", func(t *testing.T) { compatNetworkCreateInspectListRemove(t, daemon) })
	t.Run("ContainerReuse", func(t *testing.T) { compatContainerReuse(t, daemon) })
	t.Run("ContainerLifecycle", func(t *testing.T) { compatContainerLifecycle(t, daemon) })
}

// configureTestcontainers points the library at the shared daemon socket and
// pins the API version, then proves the reaper is disabled.
func configureTestcontainers(t *testing.T, daemon *daemonProcess) {
	t.Helper()
	t.Setenv("DOCKER_HOST", daemon.Host())
	// testcontainers' Moby client does not negotiate the API version, so pin
	// it to the version this daemon advertises.
	t.Setenv("DOCKER_API_VERSION", "1.44")
	// Published ports are reachable on the host's own addresses (the CNI
	// portmap DNAT chain runs from both PREROUTING and OUTPUT), not through a
	// userland proxy on loopback. testcontainers documents
	// TESTCONTAINERS_HOST_OVERRIDE for exactly this case.
	host := hostPublishAddress(t)
	t.Setenv("TESTCONTAINERS_HOST_OVERRIDE", host)
	t.Logf("published ports are consumed through host address %s", host)
	if config := testcontainers.ReadConfig(); !config.RyukDisabled {
		t.Fatalf("testcontainers Ryuk must be disabled, config=%+v", config)
	}
}

// hostPublishAddress returns the first global unicast IPv4 address of an
// up interface, preferring non-loopback interfaces.
func hostPublishAddress(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("list interfaces: %v", err)
	}
	var loopbackFallback string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || !ip.IsGlobalUnicast() {
				continue
			}
			if iface.Flags&net.FlagLoopback == 0 {
				return ip.String()
			}
			if loopbackFallback == "" {
				loopbackFallback = ip.String()
			}
		}
	}
	if loopbackFallback != "" {
		return loopbackFallback
	}
	t.Fatal("no global unicast IPv4 address available for TESTCONTAINERS_HOST_OVERRIDE")
	return ""
}

// testcontainersProvider returns a testcontainers DockerProvider bound to the
// daemon environment configured by configureTestcontainers.
func testcontainersProvider(t *testing.T) testcontainers.GenericProvider {
	t.Helper()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		t.Fatalf("testcontainers DockerProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

// requireRegistryEgress skips the pull assertions when containerd cannot reach
// a registry. The host shell has an HTTP proxy, but containerd and BuildKit
// run without one, so the probe dials directly just as containerd would.
func requireRegistryEgress(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", compatRegistryAddress, compatRegistryProbe)
	if err != nil {
		t.Skipf("testcontainers image pull needs direct registry egress from containerd (%s, no proxy configured): %v",
			compatRegistryAddress, err)
	}
	_ = conn.Close()
}

// publishedHostPort returns the daemon-reported host port for one container
// port number.
func publishedHostPort(t *testing.T, inspect *dockertypes.InspectResponse, number uint16) string {
	t.Helper()
	if inspect.NetworkSettings == nil {
		t.Fatalf("container inspect has no NetworkSettings: %+v", inspect)
	}
	for port, bindings := range inspect.NetworkSettings.Ports {
		if port.Num() != number || len(bindings) == 0 {
			continue
		}
		return bindings[0].HostPort
	}
	t.Fatalf("container inspect has no published %d/tcp binding: %+v", number, inspect.NetworkSettings.Ports)
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
