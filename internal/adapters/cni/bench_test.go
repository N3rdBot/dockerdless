package cni

import (
	"testing"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

// sequentialProbe is a deterministic, allocation-cheap replacement for the
// OS-level bind probe: benchmarks must not touch the network or the port
// namespace, and must return stable ports across runs.
func sequentialProbe(next *uint16) PortProbe {
	return func(_, _ string, requested uint16) (uint16, error) {
		if requested != 0 {
			return requested, nil
		}
		*next++
		if *next < 20000 {
			*next = 20000
		}
		return *next, nil
	}
}

// BenchmarkPortAllocatorAllocateDynamic measures the dynamic allocation path
// (requested port 0) that runs for every `-P`/`ExposedPorts` container create.
func BenchmarkPortAllocatorAllocateDynamic(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var next uint16
		allocator := NewPortAllocator(WithPortProbe(sequentialProbe(&next)))
		if _, err := allocator.Allocate("tcp", "0.0.0.0", 0); err != nil {
			b.Fatalf("Allocate: %v", err)
		}
	}
}

// BenchmarkPortAllocatorAllocateFixed measures the fixed-port validation and
// reservation path.
func BenchmarkPortAllocatorAllocateFixed(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var next uint16
		allocator := NewPortAllocator(WithPortProbe(sequentialProbe(&next)))
		if _, err := allocator.Allocate("tcp", "127.0.0.1", 8080); err != nil {
			b.Fatalf("Allocate: %v", err)
		}
	}
}

// BenchmarkPortAllocatorAllocateWithExistingReservations measures conflict
// scanning as the reservation set grows to a realistic per-host size.
func BenchmarkPortAllocatorAllocateWithExistingReservations(b *testing.B) {
	const existing = 1024
	b.ReportAllocs()
	for b.Loop() {
		var next uint16
		allocator := NewPortAllocator(WithPortProbe(sequentialProbe(&next)))
		for port := uint16(30000); port < 30000+existing; port++ {
			if _, err := allocator.Allocate("tcp", "0.0.0.0", port); err != nil {
				b.Fatalf("seed Allocate(%d): %v", port, err)
			}
		}
		if _, err := allocator.Allocate("tcp", "0.0.0.0", 0); err != nil {
			b.Fatalf("Allocate: %v", err)
		}
	}
}

// BenchmarkPortAllocatorRelease measures the map-delete release path that runs
// on every container remove.
func BenchmarkPortAllocatorRelease(b *testing.B) {
	const reserved = 256
	allocs := make([]PortAllocation, 0, reserved)
	var next uint16
	allocator := NewPortAllocator(WithPortProbe(sequentialProbe(&next)))
	for port := uint16(40000); port < 40000+reserved; port++ {
		alloc, err := allocator.Allocate("tcp", "0.0.0.0", port)
		if err != nil {
			b.Fatalf("seed Allocate(%d): %v", port, err)
		}
		allocs = append(allocs, alloc)
	}

	b.ReportAllocs()
	for b.Loop() {
		allocator.Release(allocs...)
		for _, alloc := range allocs {
			allocator.used[portKey{alloc.Protocol, alloc.HostIP, alloc.HostPort}] = struct{}{}
		}
	}
}

// BenchmarkBuildPortMappings measures the create-time projection of domain
// bindings into the CNI portmap capability payload.
func BenchmarkBuildPortMappings(b *testing.B) {
	bindings := make([]domain.PortBinding, 0, 8)
	for index := range 8 {
		bindings = append(bindings, domain.PortBinding{
			HostIP:        "0.0.0.0",
			HostPort:      uint16(8000 + index),
			ContainerPort: uint16(80 + index),
			Protocol:      "tcp",
		})
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := BuildPortMappings(bindings); err != nil {
			b.Fatalf("BuildPortMappings: %v", err)
		}
	}
}

// BenchmarkNormalizeProtocol measures the per-binding protocol normalization
// used by every allocate and mapping call.
func BenchmarkNormalizeProtocol(b *testing.B) {
	inputs := []string{"tcp", "TCP", " udp ", ""}
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		if _, err := normalizeProtocol(inputs[index%len(inputs)]); err != nil {
			b.Fatalf("normalizeProtocol: %v", err)
		}
	}
}

// BenchmarkPortAllocatorAllocateBindings measures a multi-port create request
// with rollback-free success, the common compose-style shape.
func BenchmarkPortAllocatorAllocateBindings(b *testing.B) {
	bindings := []domain.PortBinding{
		{HostPort: 0, ContainerPort: 8080, Protocol: "tcp"},
		{HostPort: 0, ContainerPort: 8443, Protocol: "tcp"},
		{HostPort: 0, ContainerPort: 5353, Protocol: "udp"},
	}
	b.ReportAllocs()
	for b.Loop() {
		var next uint16
		allocator := NewPortAllocator(WithPortProbe(sequentialProbe(&next)))
		if _, _, err := allocator.AllocateBindings(bindings); err != nil {
			b.Fatalf("AllocateBindings: %v", err)
		}
	}
}
