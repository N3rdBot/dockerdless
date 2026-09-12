package cni

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/containernetworking/cni/libcni"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

const (
	// DefaultNetConfDir is the default CNI network configuration directory.
	DefaultNetConfDir = "/etc/cni/net.d"
	// DefaultPluginDir is the default CNI plugin binary directory.
	DefaultPluginDir = "/opt/cni/bin"
	// DefaultNetworkName is Docker's default network name.
	DefaultNetworkName = "bridge"

	networkFilePrefix   = "dockerdless-"
	networkFileExt      = ".conflist"
	bridgePrefix        = "dls"
	defaultSubnetPool   = "10.88.0.0/16"
	defaultSubnetBits   = 24
	bridgeNameSuffixLen = 10
)

var networkNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type networkConfigFile struct {
	CNIVersion        string            `json:"cniVersion,omitempty"`
	Name              string            `json:"name"`
	DockerdlessID     string            `json:"dockerdlessID,omitempty"`
	DockerdlessLabels map[string]string `json:"dockerdlessLabels,omitempty"`
	Plugins           []pluginConfig    `json:"plugins"`
}

type pluginConfig struct {
	Type         string          `json:"type"`
	Bridge       string          `json:"bridge,omitempty"`
	IsGateway    bool            `json:"isGateway,omitempty"`
	IPMasq       bool            `json:"ipMasq,omitempty"`
	HairpinMode  bool            `json:"hairpinMode,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	IPAM         *ipamConfig     `json:"ipam,omitempty"`
}

type ipamConfig struct {
	Type    string        `json:"type,omitempty"`
	Ranges  [][]rangeItem `json:"ranges,omitempty"`
	Gateway string        `json:"gateway,omitempty"`
	Routes  []routeItem   `json:"routes,omitempty"`
	DataDir string        `json:"dataDir,omitempty"`
}

type rangeItem struct {
	Subnet  string `json:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

type routeItem struct {
	Dst string `json:"dst"`
}

func networkIDForName(name string) domain.NetworkID {
	sum := sha256.Sum256([]byte("dockerdless-network:" + name))
	return domain.NetworkID(hex.EncodeToString(sum[:]))
}

func bridgeInterfaceName(name string) string {
	sum := sha256.Sum256([]byte("dockerdless-bridge:" + name))
	return bridgePrefix + hex.EncodeToString(sum[:])[:bridgeNameSuffixLen]
}

func validateNetworkName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: name must not be empty", ErrInvalidNetworkName)
	case len(name) > 128:
		return fmt.Errorf("%w: %q exceeds 128 characters", ErrInvalidNetworkName, name)
	case !networkNamePattern.MatchString(name):
		return fmt.Errorf("%w: %q must match %s", ErrInvalidNetworkName, name, networkNamePattern)
	default:
		return nil
	}
}

func isReservedNetworkName(name string) bool {
	switch strings.ToLower(name) {
	case hostNetworkName, noneNetworkName:
		return true
	default:
		return false
	}
}

func networkFilePath(confDir, name string) string {
	return filepath.Join(confDir, networkFilePrefix+name+networkFileExt)
}

func writeFileExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("cni: writing %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("cni: writing %q: %w", path, err)
	}
	return nil
}

func buildBridgeNetworkConfig(name, subnet, gateway, ipamDataDir string, labels map[string]string) ([]byte, error) {
	if subnet == "" || gateway == "" {
		return nil, errors.New("cni: bridge network requires a subnet and gateway")
	}
	config := networkConfigFile{
		CNIVersion:        "1.0.0",
		Name:              name,
		DockerdlessID:     string(networkIDForName(name)),
		DockerdlessLabels: labels,
		Plugins: []pluginConfig{
			{
				Type:        driverBridge,
				Bridge:      bridgeInterfaceName(name),
				IsGateway:   true,
				IPMasq:      true,
				HairpinMode: true,
				IPAM: &ipamConfig{
					Type:    "host-local",
					Ranges:  [][]rangeItem{{{Subnet: subnet, Gateway: gateway}}},
					Gateway: gateway,
					Routes:  []routeItem{{Dst: "0.0.0.0/0"}},
					DataDir: ipamDataDir,
				},
			},
			{
				Type:         "portmap",
				Capabilities: map[string]bool{"portMappings": true},
			},
			{Type: "firewall"},
		},
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cni: encoding network config: %w", err)
	}
	return raw, nil
}

func parseNetworkFile(path string) (Network, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Network{}, fmt.Errorf("cni: reading %q: %w", path, err)
	}
	var raw networkConfigFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return Network{}, fmt.Errorf("cni: parsing %q: %w", path, err)
	}
	if raw.Name == "" {
		return Network{}, fmt.Errorf("cni: parsing %q: missing network name", path)
	}
	network := Network{
		ID:     domain.NetworkID(raw.DockerdlessID),
		Name:   raw.Name,
		Driver: driverBridge,
		Mode:   ModeNetwork,
		File:   path,
		Labels: raw.DockerdlessLabels,
	}
	if network.ID == "" {
		network.ID = networkIDForName(raw.Name)
	}
	for _, plugin := range raw.Plugins {
		if plugin.Type != "" && plugin.Bridge == "" {
			network.Driver = plugin.Type
			continue
		}
		if plugin.Bridge == "" {
			continue
		}
		network.Driver = driverBridge
		network.Bridge = plugin.Bridge
		if plugin.IPAM != nil {
			network.Gateway = plugin.IPAM.Gateway
			if len(plugin.IPAM.Ranges) > 0 && len(plugin.IPAM.Ranges[0]) > 0 {
				network.Subnet = plugin.IPAM.Ranges[0][0].Subnet
				if network.Gateway == "" {
					network.Gateway = plugin.IPAM.Ranges[0][0].Gateway
				}
			}
		}
		break
	}
	return network, nil
}

func (a *Adapter) allocateSubnet(requestedSubnet, requestedGateway string) (string, string, error) {
	if requestedSubnet != "" {
		prefix, err := netip.ParsePrefix(requestedSubnet)
		if err != nil {
			return "", "", fmt.Errorf("cni: invalid subnet %q: %w", requestedSubnet, err)
		}
		if !prefix.Addr().Is4() {
			return "", "", fmt.Errorf("%w: %q", ErrIPv6Unsupported, requestedSubnet)
		}
		prefix = prefix.Masked()
		gateway := requestedGateway
		if gateway == "" {
			gateway = defaultGatewayFor(prefix)
		} else if _, err := netip.ParseAddr(gateway); err != nil {
			return "", "", fmt.Errorf("cni: invalid gateway %q: %w", gateway, err)
		}
		return prefix.String(), gateway, nil
	}
	used, err := a.usedSubnets()
	if err != nil {
		return "", "", err
	}
	pool, err := netip.ParsePrefix(defaultSubnetPool)
	if err != nil {
		return "", "", fmt.Errorf("cni: invalid default pool: %w", err)
	}
	pool = pool.Masked()
	step := uint32(1) << (32 - defaultSubnetBits)
	for addr := pool.Addr(); pool.Contains(addr); addr = addIPv4(addr, step) {
		candidate := netip.PrefixFrom(addr, defaultSubnetBits)
		if _, taken := used[candidate.String()]; taken {
			continue
		}
		return candidate.String(), defaultGatewayFor(candidate), nil
	}
	return "", "", fmt.Errorf("cni: no free subnet in %s", defaultSubnetPool)
}

func (a *Adapter) usedSubnets() (map[string]struct{}, error) {
	files, err := libcni.ConfFiles(a.confDir, []string{networkFileExt, ".conf"})
	if err != nil {
		return nil, fmt.Errorf("cni: listing %q: %w", a.confDir, err)
	}
	used := make(map[string]struct{}, len(files))
	for _, file := range files {
		raw, readErr := os.ReadFile(file)
		if readErr != nil {
			continue
		}
		var document networkConfigFile
		if json.Unmarshal(raw, &document) != nil {
			continue
		}
		for _, plugin := range document.Plugins {
			if plugin.IPAM == nil {
				continue
			}
			for _, group := range plugin.IPAM.Ranges {
				for _, item := range group {
					if item.Subnet != "" {
						used[item.Subnet] = struct{}{}
					}
				}
			}
		}
	}
	return used, nil
}

func addIPv4(addr netip.Addr, delta uint32) netip.Addr {
	octets := addr.As4()
	value := uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])
	value += delta
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}

func defaultGatewayFor(prefix netip.Prefix) string {
	return addIPv4(prefix.Masked().Addr(), 1).String()
}
