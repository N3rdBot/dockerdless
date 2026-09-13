package cni

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

const (
	protocolTCP = "tcp"
	protocolUDP = "udp"

	defaultHostIP          = "0.0.0.0"
	defaultProbeRetryLimit = 16
)

var (
	// ErrPortInUse reports a requested host port that cannot be reserved.
	ErrPortInUse = errors.New("host port is already allocated")
	// ErrNoFreeHostPort reports that dynamic allocation found no free port.
	ErrNoFreeHostPort = errors.New("no free host port found")
	// ErrUnallocatedPort rejects a port binding that still has host port 0.
	ErrUnallocatedPort = errors.New("port binding has no allocated host port")
	ErrPortNotReserved = errors.New("host port is not reserved")
	// ErrUnsupportedProtocol rejects protocols other than tcp and udp.
	ErrUnsupportedProtocol = errors.New("unsupported port protocol")
)

// PortInUseError describes the binding that collided.
type PortInUseError struct {
	Protocol string
	HostIP   string
	HostPort uint16
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("%s %s:%d: %s", e.Protocol, e.HostIP, e.HostPort, ErrPortInUse)
}

// Is lets errors.Is(err, ErrPortInUse) match.
func (e *PortInUseError) Is(target error) bool {
	return target == ErrPortInUse
}

// PortMapping is one entry of the CNI portmap capability runtimeConfig.
type PortMapping struct {
	HostPort      int32  `json:"hostPort"`
	ContainerPort int32  `json:"containerPort"`
	Protocol      string `json:"protocol"`
	HostIP        string `json:"hostIP,omitempty"`
}

// PortAllocation is one reserved host port.
type PortAllocation struct {
	Protocol string
	HostIP   string
	HostPort uint16
}

type portKey struct {
	protocol string
	hostIP   string
	port     uint16
}

// PortProbe returns a concrete free port for the request; requested 0 means
// "any free port", otherwise the probe must verify the exact port.
type PortProbe func(protocol, hostIP string, requested uint16) (uint16, error)

// PortAllocatorOption customizes a PortAllocator.
type PortAllocatorOption func(*PortAllocator)

// WithPortProbe overrides the OS-level bind probe (used by tests and by
// environments where binding is not permitted).
func WithPortProbe(probe PortProbe) PortAllocatorOption {
	return func(a *PortAllocator) {
		if probe != nil {
			a.probe = probe
		}
	}
}

// PortAllocator reserves concrete host ports before CNI portmap runs, so the
// published port Docker reports is always nonzero and stable.
type PortAllocator struct {
	mu       sync.Mutex
	used     map[portKey]struct{}
	probe    PortProbe
	attempts int
}

// NewPortAllocator returns an allocator backed by bind-and-read-back probing.
func NewPortAllocator(opts ...PortAllocatorOption) *PortAllocator {
	a := &PortAllocator{
		used:     make(map[portKey]struct{}),
		probe:    probeFreeHostPort,
		attempts: defaultProbeRetryLimit,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Allocate reserves one host port for the protocol. A requested port of 0 asks
// the OS for a free port; any other value is validated and reserved exactly.
func (a *PortAllocator) Allocate(protocol, hostIP string, requested uint16) (PortAllocation, error) {
	proto, err := normalizeProtocol(protocol)
	if err != nil {
		return PortAllocation{}, err
	}
	ip := normalizeHostIP(hostIP)

	a.mu.Lock()
	defer a.mu.Unlock()

	if requested != 0 {
		if a.conflictsLocked(proto, ip, requested) {
			return PortAllocation{}, &PortInUseError{Protocol: proto, HostIP: ip, HostPort: requested}
		}
		port, probeErr := a.probe(proto, ip, requested)
		if probeErr != nil {
			if isAddrInUse(probeErr) {
				return PortAllocation{}, &PortInUseError{Protocol: proto, HostIP: ip, HostPort: requested}
			}
			return PortAllocation{}, fmt.Errorf("cni: probing host port %d: %w", requested, probeErr)
		}
		if port != requested {
			return PortAllocation{}, fmt.Errorf("cni: probe returned port %d for fixed request %d: %w", port, requested, ErrNoFreeHostPort)
		}
		a.used[portKey{proto, ip, port}] = struct{}{}
		return PortAllocation{Protocol: proto, HostIP: ip, HostPort: port}, nil
	}

	for attempt := 0; attempt < a.attempts; attempt++ {
		port, probeErr := a.probe(proto, ip, 0)
		if probeErr != nil {
			return PortAllocation{}, fmt.Errorf("cni: probing free host port: %w", probeErr)
		}
		if port == 0 {
			return PortAllocation{}, ErrNoFreeHostPort
		}
		if a.conflictsLocked(proto, ip, port) {
			continue
		}
		a.used[portKey{proto, ip, port}] = struct{}{}
		return PortAllocation{Protocol: proto, HostIP: ip, HostPort: port}, nil
	}
	return PortAllocation{}, fmt.Errorf("%w after %d attempts", ErrNoFreeHostPort, a.attempts)
}

// Release frees previously allocated ports.
func (a *PortAllocator) Release(allocs ...PortAllocation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, alloc := range allocs {
		proto, err := normalizeProtocol(alloc.Protocol)
		if err != nil {
			continue
		}
		delete(a.used, portKey{proto, normalizeHostIP(alloc.HostIP), alloc.HostPort})
	}
}

func (a *PortAllocator) AdoptBindings(bindings []domain.PortBinding) ([]PortAllocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	allocations := make([]PortAllocation, 0, len(bindings))
	for _, binding := range bindings {
		protocol, err := normalizeProtocol(binding.Protocol)
		if err != nil {
			return nil, err
		}
		allocation := PortAllocation{Protocol: protocol, HostIP: normalizeHostIP(binding.HostIP), HostPort: binding.HostPort}
		if _, ok := a.used[portKey{protocol, allocation.HostIP, allocation.HostPort}]; !ok {
			return nil, fmt.Errorf("cni: %s:%d: %w", allocation.HostIP, allocation.HostPort, ErrPortNotReserved)
		}
		allocations = append(allocations, allocation)
	}
	return allocations, nil
}

// IsUsed reports whether the host port is reserved, honoring wildcard IPs.
func (a *PortAllocator) IsUsed(protocol, hostIP string, port uint16) bool {
	proto, err := normalizeProtocol(protocol)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conflictsLocked(proto, normalizeHostIP(hostIP), port)
}

// AllocateBindings gives every binding a concrete reservation, rolling back
// all earlier reservations if any binding fails.
func (a *PortAllocator) AllocateBindings(bindings []domain.PortBinding) ([]domain.PortBinding, []PortAllocation, error) {
	filled := make([]domain.PortBinding, 0, len(bindings))
	allocs := make([]PortAllocation, 0, len(bindings))
	for _, binding := range bindings {
		alloc, err := a.Allocate(binding.Protocol, binding.HostIP, binding.HostPort)
		if err != nil {
			a.Release(allocs...)
			return nil, nil, fmt.Errorf("cni: host port for container port %d: %w", binding.ContainerPort, err)
		}
		allocs = append(allocs, alloc)
		next := binding
		next.Protocol = alloc.Protocol
		next.HostIP = alloc.HostIP
		next.HostPort = alloc.HostPort
		filled = append(filled, next)
	}
	return filled, allocs, nil
}

func (a *PortAllocator) conflictsLocked(protocol, hostIP string, port uint16) bool {
	for key := range a.used {
		if key.protocol != protocol || key.port != port {
			continue
		}
		if key.hostIP == hostIP || isWildcardHost(key.hostIP) || isWildcardHost(hostIP) {
			return true
		}
	}
	return false
}

// BuildPortMappings converts concrete bindings into the CNI portmap
// capability payload, refusing to forward Docker's host_port=0 sentinel.
func BuildPortMappings(bindings []domain.PortBinding) ([]PortMapping, error) {
	mappings := make([]PortMapping, 0, len(bindings))
	for _, binding := range bindings {
		if binding.HostPort == 0 {
			return nil, fmt.Errorf("container port %d/%s: %w", binding.ContainerPort, binding.Protocol, ErrUnallocatedPort)
		}
		if binding.ContainerPort == 0 {
			return nil, fmt.Errorf("host port %d: container port is zero: %w", binding.HostPort, ErrUnallocatedPort)
		}
		proto, err := normalizeProtocol(binding.Protocol)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, PortMapping{
			HostPort:      int32(binding.HostPort),
			ContainerPort: int32(binding.ContainerPort),
			Protocol:      proto,
			HostIP:        normalizeHostIP(binding.HostIP),
		})
	}
	return mappings, nil
}

func normalizeProtocol(protocol string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", protocolTCP:
		return protocolTCP, nil
	case protocolUDP:
		return protocolUDP, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
	}
}

func normalizeHostIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return defaultHostIP
	}
	return ip
}

func isWildcardHost(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed == nil || parsed.IsUnspecified()
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

// probeFreeHostPort binds a throwaway socket to discover/verify a port. The
// socket is closed immediately; the reservation is tracked by the allocator.
func probeFreeHostPort(protocol, hostIP string, requested uint16) (uint16, error) {
	addr := net.JoinHostPort(hostIP, strconv.Itoa(int(requested)))
	switch protocol {
	case protocolUDP:
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close() }()
		udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
		if !ok {
			return 0, fmt.Errorf("cni: unexpected UDP local address %T", conn.LocalAddr())
		}
		return uint16(udpAddr.Port), nil
	case protocolTCP:
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			return 0, err
		}
		defer func() { _ = listener.Close() }()
		tcpAddr, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			return 0, fmt.Errorf("cni: unexpected TCP local address %T", listener.Addr())
		}
		return uint16(tcpAddr.Port), nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
	}
}
