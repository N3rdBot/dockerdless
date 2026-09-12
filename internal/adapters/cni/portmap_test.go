package cni_test

import (
	"errors"
	"syscall"
	"testing"

	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
)

func stubProbe(fn func(protocol, hostIP string, requested uint16) (uint16, error)) cni.PortAllocatorOption {
	return cni.WithPortProbe(fn)
}

// TestAllocateDynamicPortIsNonzero proves Docker's host_port=0 request yields
// a concrete, reserved, nonzero host port.
func TestAllocateDynamicPortIsNonzero(t *testing.T) {
	a := cni.NewPortAllocator()

	first, err := a.Allocate("tcp", "", 0)
	if err != nil {
		t.Fatalf("Allocate(0): %v", err)
	}
	if first.HostPort == 0 {
		t.Fatal("Allocate(0) returned host port 0; Docker requests must be resolved to a concrete port")
	}
	if first.HostIP != "0.0.0.0" {
		t.Fatalf("HostIP = %q, want 0.0.0.0", first.HostIP)
	}
	if first.Protocol != "tcp" {
		t.Fatalf("Protocol = %q, want tcp", first.Protocol)
	}
	if !a.IsUsed("tcp", "0.0.0.0", first.HostPort) {
		t.Fatalf("port %d is not tracked after allocation", first.HostPort)
	}

	second, err := a.Allocate("tcp", "", 0)
	if err != nil {
		t.Fatalf("Allocate(0) second: %v", err)
	}
	if second.HostPort == first.HostPort {
		t.Fatalf("second dynamic allocation reused port %d", first.HostPort)
	}

	a.Release(first, second)
	if a.IsUsed("tcp", "0.0.0.0", first.HostPort) {
		t.Fatalf("port %d still tracked after Release", first.HostPort)
	}
}

// TestAllocateRetriesWhenProbeCollides proves the allocator retries a dynamic
// allocation instead of publishing a port it cannot own.
func TestAllocateRetriesWhenProbeCollides(t *testing.T) {
	var calls int
	a := cni.NewPortAllocator(stubProbe(func(_, _ string, requested uint16) (uint16, error) {
		calls++
		if calls < 3 {
			return 51000, nil
		}
		return 51001, nil
	}))

	first, err := a.Allocate("tcp", "", 0)
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	if first.HostPort != 51000 {
		t.Fatalf("first port = %d, want 51000", first.HostPort)
	}
	second, err := a.Allocate("tcp", "", 0)
	if err != nil {
		t.Fatalf("second Allocate should have retried past the collision: %v", err)
	}
	if second.HostPort != 51001 {
		t.Fatalf("second port = %d, want 51001 after retry", second.HostPort)
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d, want 3 (two collisions then success)", calls)
	}
}

// TestAllocateFixedPortCollisionFailsCleanly proves a requested host port that
// is already allocated fails with ErrPortInUse and does not leak an allocation.
func TestAllocateFixedPortCollisionFailsCleanly(t *testing.T) {
	a := cni.NewPortAllocator(stubProbe(func(_, _ string, requested uint16) (uint16, error) {
		if requested == 0 {
			return 52000, nil
		}
		return requested, nil
	}))

	if _, err := a.Allocate("tcp", "", 0); err != nil {
		t.Fatalf("prime allocation: %v", err)
	}

	_, err := a.Allocate("tcp", "", 52000)
	if !errors.Is(err, cni.ErrPortInUse) {
		t.Fatalf("Allocate(collision) = %v, want ErrPortInUse", err)
	}
	if !a.IsUsed("tcp", "0.0.0.0", 52000) {
		t.Fatal("failed allocation dropped the pre-existing reservation")
	}

	next, err := a.Allocate("tcp", "", 52001)
	if err != nil {
		t.Fatalf("unrelated port after collision: %v", err)
	}
	if next.HostPort != 52001 {
		t.Fatalf("next port = %d, want 52001", next.HostPort)
	}
}

// TestAllocateFixedPortBindFailureIsPortInUse proves an OS-level bind failure
// (address already in use) maps to ErrPortInUse rather than a generic error.
func TestAllocateFixedPortBindFailureIsPortInUse(t *testing.T) {
	a := cni.NewPortAllocator(stubProbe(func(_, _ string, _ uint16) (uint16, error) {
		return 0, syscall.EADDRINUSE
	}))
	_, err := a.Allocate("tcp", "", 8080)
	if !errors.Is(err, cni.ErrPortInUse) {
		t.Fatalf("Allocate(bind EADDRINUSE) = %v, want ErrPortInUse", err)
	}
}

// TestAllocateBindingsReleasesPartialAllocationsOnFailure proves a mid-list
// failure rolls back every port allocated earlier in the call.
func TestAllocateBindingsReleasesPartialAllocationsOnFailure(t *testing.T) {
	a := cni.NewPortAllocator(stubProbe(func(_, _ string, requested uint16) (uint16, error) {
		if requested == 0 {
			return 53000, nil
		}
		return requested, nil
	}))

	bindings := []domain.PortBinding{
		{ContainerPort: 80, Protocol: "tcp", HostPort: 0},
		{ContainerPort: 81, Protocol: "tcp", HostPort: 53000},
	}
	_, _, err := a.AllocateBindings(bindings)
	if !errors.Is(err, cni.ErrPortInUse) {
		t.Fatalf("AllocateBindings = %v, want ErrPortInUse", err)
	}
	if a.IsUsed("tcp", "0.0.0.0", 53000) {
		t.Fatal("partial allocation 53000 leaked after failure")
	}
}

// TestAllocateBindingsFillsConcretePorts proves the Docker-facing result has a
// nonzero published port for every requested binding.
func TestAllocateBindingsFillsConcretePorts(t *testing.T) {
	a := cni.NewPortAllocator()
	bindings := []domain.PortBinding{
		{ContainerPort: 80, Protocol: "", HostPort: 0},
		{ContainerPort: 53, Protocol: "udp", HostPort: 0},
	}
	filled, allocs, err := a.AllocateBindings(bindings)
	if err != nil {
		t.Fatalf("AllocateBindings: %v", err)
	}
	if len(filled) != 2 || len(allocs) != 2 {
		t.Fatalf("len(filled)=%d len(allocs)=%d, want 2/2", len(filled), len(allocs))
	}
	for i, b := range filled {
		if b.HostPort == 0 {
			t.Fatalf("binding %d still has host port 0", i)
		}
		if b.Protocol != bindings[i].Protocol && !(bindings[i].Protocol == "" && b.Protocol == "tcp") {
			t.Fatalf("binding %d protocol = %q, want %q", i, b.Protocol, bindings[i].Protocol)
		}
	}
	a.Release(allocs...)
}

// TestBuildPortMappingsRejectsZeroHostPort proves the CNI portmap capability is
// never handed Docker's host_port=0 sentinel.
func TestBuildPortMappingsRejectsZeroHostPort(t *testing.T) {
	_, err := cni.BuildPortMappings([]domain.PortBinding{{ContainerPort: 80, Protocol: "tcp"}})
	if !errors.Is(err, cni.ErrUnallocatedPort) {
		t.Fatalf("BuildPortMappings(host port 0) = %v, want ErrUnallocatedPort", err)
	}
}

func TestBuildPortMappingsNormalizesAndCopies(t *testing.T) {
	mappings, err := cni.BuildPortMappings([]domain.PortBinding{
		{ContainerPort: 80, Protocol: "", HostIP: "", HostPort: 8080},
		{ContainerPort: 443, Protocol: "TCP", HostIP: "127.0.0.1", HostPort: 8443},
	})
	if err != nil {
		t.Fatalf("BuildPortMappings: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("len = %d, want 2", len(mappings))
	}
	if mappings[0].Protocol != "tcp" || mappings[0].HostIP != "0.0.0.0" {
		t.Fatalf("mapping 0 = %+v, want tcp/0.0.0.0", mappings[0])
	}
	if mappings[0].HostPort != 8080 || mappings[0].ContainerPort != 80 {
		t.Fatalf("mapping 0 ports = %+v, want 80->8080", mappings[0])
	}
	if mappings[1].Protocol != "tcp" || mappings[1].HostIP != "127.0.0.1" || mappings[1].HostPort != 8443 {
		t.Fatalf("mapping 1 = %+v", mappings[1])
	}
}
