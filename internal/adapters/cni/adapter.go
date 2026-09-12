// Package cni provides the CNI network adapter.
package cni

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/containernetworking/cni/libcni"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

const (
	driverBridge    = "bridge"
	defaultIfName   = "eth0"
	hostNetworkName = "host"
	noneNetworkName = "none"
)

var (
	// ErrNetworkNotFound reports an unknown network name or id.
	ErrNetworkNotFound = errors.New("network not found")
	// ErrNetworkExists reports a duplicate network name.
	ErrNetworkExists = errors.New("network already exists")
	// ErrNetworkInUse reports removal of a network with active endpoints.
	ErrNetworkInUse = errors.New("network has active endpoints")
	// ErrInvalidNetworkName reports a name Docker would reject.
	ErrInvalidNetworkName = errors.New("invalid network name")
	// ErrReservedNetworkName reports a predefined network name.
	ErrReservedNetworkName = errors.New("network name is reserved")
	// ErrUnsupportedNetworkDriver reports a driver other than bridge.
	ErrUnsupportedNetworkDriver = errors.New("unsupported network driver")
	// ErrUnknownNetworkMode reports a mode token dockerdless cannot serve.
	ErrUnknownNetworkMode = errors.New("unknown network mode")
	// ErrPortPublishWithMode rejects published ports on host/none networks.
	ErrPortPublishWithMode = errors.New("port publishing is not supported with this network mode")
	// ErrMissingNetNS reports a connect request without a netns path.
	ErrMissingNetNS = errors.New("container netns path is required")
	// ErrMissingContainer reports a connect/disconnect without a container id.
	ErrMissingContainer = errors.New("container id is required")
	// ErrIPv6Unsupported reports a request for IPv6 addressing.
	ErrIPv6Unsupported = errors.New("IPv6 networks are not supported yet")

	errLinkNotFound = errors.New("cni: network interface not found")
)

// Mode classifies how a container joins a network.
type Mode string

const (
	// ModeHost shares the host network namespace.
	ModeHost Mode = hostNetworkName
	// ModeNone attaches no network.
	ModeNone Mode = noneNetworkName
	// ModeNetwork attaches a named CNI network.
	ModeNetwork Mode = "network"
)

// ParseNetworkMode classifies a Docker network mode token. Container and
// namespace sharing modes are explicitly rejected so callers can return
// Docker's 400 for them; unknown names stay ModeNetwork and later surface
// ErrNetworkNotFound (Docker's 404).
func ParseNetworkMode(mode string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "default", driverBridge:
		return ModeNetwork, nil
	case hostNetworkName:
		return ModeHost, nil
	case noneNetworkName:
		return ModeNone, nil
	}
	if strings.Contains(mode, ":") {
		return "", fmt.Errorf("%w: %q", ErrUnknownNetworkMode, mode)
	}
	return ModeNetwork, nil
}

// Network is one Docker-visible network.
type Network struct {
	ID      domain.NetworkID
	Name    string
	Driver  string
	Mode    Mode
	File    string
	Bridge  string
	Subnet  string
	Gateway string
	Labels  map[string]string
}

// CreateNetworkRequest describes a network create request.
type CreateNetworkRequest struct {
	Name       string
	Driver     string
	Subnet     string
	Gateway    string
	EnableIPv6 bool
	Labels     map[string]string
}

// ConnectRequest describes attaching a container to a network.
type ConnectRequest struct {
	Network   string
	Container domain.ContainerID
	NetNS     string
	IfName    string
	Ports     []domain.PortBinding
	Aliases   []string
}

// ConnectResult carries the attached endpoint plus the concrete port
// allocations that were configured in CNI portmap.
type ConnectResult struct {
	Attachment domain.NetworkAttachment
	Ports      []domain.PortBinding
}

// DisconnectRequest describes detaching a container from a network.
type DisconnectRequest struct {
	Network   string
	Container domain.ContainerID
	NetNS     string
	IfName    string
}

// Config configures a CNI Adapter.
type Config struct {
	// NetConfDir is the CNI network configuration directory.
	NetConfDir string
	// PluginDirs are the CNI plugin binary directories.
	PluginDirs []string
	// CacheDir overrides libcni's result cache (defaults to libcni's).
	CacheDir string
	// IPAMDataDir overrides host-local's allocation store (defaults to
	// /var/lib/cni/networks). Set it for hermetic test environments.
	IPAMDataDir string
	// CNI injects a libcni implementation (tests); nil builds the real one.
	CNI libcni.CNI
	// Allocator injects a host port allocator.
	Allocator *PortAllocator
	// Probe reads live network namespace state for inspect fallbacks.
	Probe NetNSProbe
}

// Adapter is the CNI implementation of the network port.
type Adapter struct {
	confDir     string
	ipamDataDir string
	cni         libcni.CNI
	alloc       *PortAllocator
	probe       NetNSProbe

	mu          sync.Mutex
	attachments map[attachmentKey]attachmentState
}

type attachmentKey struct {
	container domain.ContainerID
	network   string
}

type attachmentState struct {
	allocations []PortAllocation
	ports       []domain.PortBinding
	netns       string
	ifName      string
}

// New constructs a CNI network adapter.
func New(cfg Config) *Adapter {
	confDir := cfg.NetConfDir
	if confDir == "" {
		confDir = DefaultNetConfDir
	}
	client := cfg.CNI
	if client == nil {
		pluginDirs := cfg.PluginDirs
		if len(pluginDirs) == 0 {
			pluginDirs = []string{DefaultPluginDir}
		}
		if cfg.CacheDir != "" {
			client = libcni.NewCNIConfigWithCacheDir(pluginDirs, cfg.CacheDir, nil)
		} else {
			client = libcni.NewCNIConfig(pluginDirs, nil)
		}
	}
	allocator := cfg.Allocator
	if allocator == nil {
		allocator = NewPortAllocator()
	}
	probe := cfg.Probe
	if probe == nil {
		probe = SetnsProbe{}
	}
	return &Adapter{
		confDir:     confDir,
		ipamDataDir: cfg.IPAMDataDir,
		cni:         client,
		alloc:       allocator,
		probe:       probe,
		attachments: make(map[attachmentKey]attachmentState),
	}
}

// Create implements ports.Network by creating a bridge-backed CNI network.
func (a *Adapter) Create(ctx context.Context, name string) (domain.NetworkID, error) {
	return a.CreateNetwork(ctx, CreateNetworkRequest{Name: name, Driver: driverBridge})
}

// CreateNetwork writes the CNI conflist backing a Docker network.
func (a *Adapter) CreateNetwork(ctx context.Context, req CreateNetworkRequest) (domain.NetworkID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := strings.TrimSpace(req.Name)
	if err := validateNetworkName(name); err != nil {
		return "", err
	}
	if isReservedNetworkName(name) {
		return "", fmt.Errorf("%w: %q", ErrReservedNetworkName, name)
	}
	driver := strings.ToLower(strings.TrimSpace(req.Driver))
	if driver == "" {
		driver = driverBridge
	}
	if driver != driverBridge {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedNetworkDriver, req.Driver)
	}
	if req.EnableIPv6 {
		return "", fmt.Errorf("%w: network %q", ErrIPv6Unsupported, name)
	}
	if err := os.MkdirAll(a.confDir, 0o755); err != nil {
		return "", fmt.Errorf("cni: creating %q: %w", a.confDir, err)
	}
	subnet, gateway, err := a.allocateSubnet(req.Subnet, req.Gateway)
	if err != nil {
		return "", err
	}
	raw, err := buildBridgeNetworkConfig(name, subnet, gateway, a.ipamDataDir, req.Labels)
	if err != nil {
		return "", err
	}
	path := networkFilePath(a.confDir, name)
	if err := writeFileExclusive(path, raw); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: %q", ErrNetworkExists, name)
		}
		return "", fmt.Errorf("cni: writing %q: %w", path, err)
	}
	return networkIDForName(name), nil
}

// List returns every network configured underneath the netconf directory.
func (a *Adapter) List(ctx context.Context) ([]Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := libcni.ConfFiles(a.confDir, []string{networkFileExt, ".conf"})
	if err != nil {
		return nil, fmt.Errorf("cni: listing %q: %w", a.confDir, err)
	}
	networks := make([]Network, 0, len(files))
	for _, file := range files {
		if !strings.HasPrefix(filepath.Base(file), networkFilePrefix) {
			continue
		}
		network, parseErr := parseNetworkFile(file)
		if parseErr != nil {
			return nil, parseErr
		}
		networks = append(networks, network)
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	return networks, nil
}

// Resolve finds a network by name, full id, or id prefix. host and none
// return their predefined descriptors.
func (a *Adapter) Resolve(ctx context.Context, ref string) (Network, error) {
	if err := ctx.Err(); err != nil {
		return Network{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = DefaultNetworkName
	}
	mode, modeErr := ParseNetworkMode(ref)
	if modeErr != nil {
		return Network{}, modeErr
	}
	switch mode {
	case ModeHost, ModeNone:
		return Network{ID: networkIDForName(ref), Name: ref, Driver: ref, Mode: mode}, nil
	}
	networks, err := a.List(ctx)
	if err != nil {
		return Network{}, err
	}
	for _, network := range networks {
		if network.Name == ref || string(network.ID) == ref {
			return network, nil
		}
	}
	if len(ref) >= 12 {
		for _, network := range networks {
			if strings.HasPrefix(string(network.ID), ref) {
				return network, nil
			}
		}
	}
	return Network{}, fmt.Errorf("%w: %q", ErrNetworkNotFound, ref)
}

// EnsureDefaultNetwork creates the default bridge network when missing.
func (a *Adapter) EnsureDefaultNetwork(ctx context.Context) (domain.NetworkID, error) {
	_, err := a.Resolve(ctx, DefaultNetworkName)
	if err == nil {
		return networkIDForName(DefaultNetworkName), nil
	}
	if !errors.Is(err, ErrNetworkNotFound) {
		return "", err
	}
	return a.CreateNetwork(ctx, CreateNetworkRequest{Name: DefaultNetworkName, Driver: driverBridge})
}

// Remove implements ports.Network by deleting a CNI conflist.
func (a *Adapter) Remove(ctx context.Context, id domain.NetworkID) error {
	network, err := a.Resolve(ctx, string(id))
	if err != nil {
		return err
	}
	if network.Mode != ModeNetwork || network.File == "" {
		return fmt.Errorf("%w: %q", ErrReservedNetworkName, network.Name)
	}
	inUse, err := a.networkInUse(network.Name)
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("%w: %q", ErrNetworkInUse, network.Name)
	}
	if network.Bridge != "" {
		if err := deleteLink(network.Bridge); err != nil && !errors.Is(err, errLinkNotFound) {
			return fmt.Errorf("cni: removing bridge %q: %w", network.Bridge, err)
		}
	}
	if err := os.Remove(network.File); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %q", ErrNetworkNotFound, network.Name)
		}
		return fmt.Errorf("cni: removing %q: %w", network.File, err)
	}
	return nil
}

func (a *Adapter) networkInUse(name string) (bool, error) {
	a.mu.Lock()
	for key := range a.attachments {
		if key.network == name {
			a.mu.Unlock()
			return true, nil
		}
	}
	a.mu.Unlock()
	cached, err := a.cni.GetCachedAttachments("")
	if err != nil {
		return false, fmt.Errorf("cni: reading cached attachments: %w", err)
	}
	for _, attachment := range cached {
		if attachment != nil && attachment.Network == name {
			return true, nil
		}
	}
	return false, nil
}

// Connect attaches a container to a network. Host and none modes skip CNI
// entirely; named networks allocate concrete host ports first, then invoke the
// CNI ADD with the portmappings capability.
func (a *Adapter) Connect(ctx context.Context, req ConnectRequest) (ConnectResult, error) {
	if err := ctx.Err(); err != nil {
		return ConnectResult{}, err
	}
	if strings.TrimSpace(string(req.Container)) == "" {
		return ConnectResult{}, ErrMissingContainer
	}
	network, err := a.Resolve(ctx, req.Network)
	if err != nil {
		return ConnectResult{}, err
	}
	switch network.Mode {
	case ModeHost, ModeNone:
		if len(req.Ports) > 0 {
			return ConnectResult{}, fmt.Errorf("%w: %s", ErrPortPublishWithMode, network.Mode)
		}
		return ConnectResult{Attachment: domain.NetworkAttachment{NetworkID: network.ID, Name: network.Name}}, nil
	}
	if network.File == "" {
		return ConnectResult{}, fmt.Errorf("%w: %q", ErrNetworkNotFound, network.Name)
	}
	if strings.TrimSpace(req.NetNS) == "" {
		return ConnectResult{}, ErrMissingNetNS
	}
	ifName := strings.TrimSpace(req.IfName)
	if ifName == "" {
		ifName = defaultIfName
	}
	list, err := libcni.NetworkConfFromFile(network.File)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("cni: loading network %q: %w", network.Name, err)
	}

	filled, allocations, err := a.alloc.AllocateBindings(req.Ports)
	if err != nil {
		return ConnectResult{}, err
	}
	rollback := true
	defer func() {
		if rollback {
			a.alloc.Release(allocations...)
		}
	}()

	mappings, err := BuildPortMappings(filled)
	if err != nil {
		return ConnectResult{}, err
	}
	capabilities := map[string]any{}
	if len(mappings) > 0 {
		capabilities["portMappings"] = mappings
	}
	runtimeConf := &libcni.RuntimeConf{
		ContainerID:    string(req.Container),
		NetNS:          req.NetNS,
		IfName:         ifName,
		CapabilityArgs: capabilities,
	}
	result, err := a.cni.AddNetworkList(ctx, list, runtimeConf)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("cni: connecting container %s to %q: %w", req.Container, network.Name, err)
	}
	attachment, err := AttachmentFromResult(network.ID, network.Name, ifName, req.Aliases, result)
	if err != nil {
		_ = a.cni.DelNetworkList(ctx, list, runtimeConf)
		return ConnectResult{}, err
	}
	if a.probe != nil {
		_ = FillFromNetNS(&attachment, a.probe, req.NetNS, ifName)
	}
	a.mu.Lock()
	a.attachments[attachmentKey{container: req.Container, network: network.Name}] = attachmentState{
		allocations: allocations,
		ports:       filled,
		netns:       req.NetNS,
		ifName:      ifName,
	}
	a.mu.Unlock()
	rollback = false
	return ConnectResult{Attachment: attachment, Ports: filled}, nil
}

// Disconnect detaches a container and always releases its port reservations,
// even when the CNI DEL itself fails, so no allocation leaks.
func (a *Adapter) Disconnect(ctx context.Context, req DisconnectRequest) error {
	network, err := a.Resolve(ctx, req.Network)
	if err != nil {
		return err
	}
	switch network.Mode {
	case ModeHost, ModeNone:
		return nil
	}
	if strings.TrimSpace(string(req.Container)) == "" {
		return ErrMissingContainer
	}
	ifName := strings.TrimSpace(req.IfName)
	if ifName == "" {
		ifName = defaultIfName
	}
	list, err := libcni.NetworkConfFromFile(network.File)
	if err != nil {
		return fmt.Errorf("cni: loading network %q: %w", network.Name, err)
	}
	key := attachmentKey{container: req.Container, network: network.Name}
	a.mu.Lock()
	state, tracked := a.attachments[key]
	a.mu.Unlock()

	runtimeConf := &libcni.RuntimeConf{
		ContainerID: string(req.Container),
		NetNS:       req.NetNS,
		IfName:      ifName,
	}
	if _, cached, cacheErr := a.cni.GetNetworkListCachedConfig(list, runtimeConf); cacheErr == nil && cached != nil {
		if len(cached.CapabilityArgs) > 0 {
			runtimeConf.CapabilityArgs = cached.CapabilityArgs
		}
		if runtimeConf.NetNS == "" {
			runtimeConf.NetNS = cached.NetNS
		}
	}
	if len(runtimeConf.CapabilityArgs) == 0 && tracked && len(state.ports) > 0 {
		if mappings, mapErr := BuildPortMappings(state.ports); mapErr == nil {
			runtimeConf.CapabilityArgs = map[string]any{"portMappings": mappings}
		}
	}
	delErr := a.cni.DelNetworkList(ctx, list, runtimeConf)

	a.mu.Lock()
	delete(a.attachments, key)
	a.mu.Unlock()
	if tracked {
		a.alloc.Release(state.allocations...)
	}
	if delErr != nil {
		return fmt.Errorf("cni: disconnecting container %s from %q: %w", req.Container, network.Name, delErr)
	}
	return nil
}

// DisconnectAll tears down every tracked attachment for a container.
func (a *Adapter) DisconnectAll(ctx context.Context, container domain.ContainerID) []error {
	a.mu.Lock()
	keys := make([]attachmentKey, 0)
	states := make(map[attachmentKey]attachmentState)
	for key, state := range a.attachments {
		if key.container == container {
			keys = append(keys, key)
			states[key] = state
		}
	}
	a.mu.Unlock()

	errs := make([]error, 0, len(keys))
	for _, key := range keys {
		state := states[key]
		err := a.Disconnect(ctx, DisconnectRequest{
			Network:   key.network,
			Container: container,
			NetNS:     state.netns,
			IfName:    state.ifName,
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

var _ ports.Network = (*Adapter)(nil)
