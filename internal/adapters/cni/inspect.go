package cni

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/containernetworking/cni/pkg/types"
	types100 "github.com/containernetworking/cni/pkg/types/100"
	"github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

// ErrNilResult reports a missing CNI plugin result.
var ErrNilResult = errors.New("cni: plugin returned no result")

// InterfaceInfo is the live state of a container interface inside a netns.
type InterfaceInfo struct {
	IPAddresses []net.IPNet
	MAC         string
}

// NetNSProbe reads interface state from a live network namespace.
type NetNSProbe interface {
	InterfaceInfo(netnsPath, ifName string) (InterfaceInfo, error)
}

// AttachmentFromResult converts a CNI plugin result into a Docker-facing
// network attachment, using the container-side interface for the MAC address.
func AttachmentFromResult(netID domain.NetworkID, name, ifName string, aliases []string, result types.Result) (domain.NetworkAttachment, error) {
	if result == nil {
		return domain.NetworkAttachment{}, ErrNilResult
	}
	decoded, err := types100.GetResult(result)
	if err != nil {
		return domain.NetworkAttachment{}, fmt.Errorf("cni: decoding plugin result: %w", err)
	}
	attachment := domain.NetworkAttachment{
		NetworkID: netID,
		Name:      name,
		Aliases:   aliases,
	}
	for _, ipConfig := range decoded.IPs {
		if ipConfig == nil || ipConfig.Address.IP == nil {
			continue
		}
		if v4 := ipConfig.Address.IP.To4(); v4 != nil {
			if attachment.IPAddress != "" {
				continue
			}
			attachment.IPAddress = v4.String()
			attachment.IPPrefixLen, _ = ipConfig.Address.Mask.Size()
			if ipConfig.Gateway != nil {
				attachment.Gateway = ipConfig.Gateway.String()
			}
			continue
		}
		if attachment.GlobalIPv6Address != "" {
			continue
		}
		attachment.GlobalIPv6Address = ipConfig.Address.IP.String()
		attachment.GlobalIPv6PrefixLen, _ = ipConfig.Address.Mask.Size()
	}
	attachment.MACAddress = containerInterfaceMAC(decoded, ifName)
	return attachment, nil
}

// containerInterfaceMAC returns the MAC of the container-side interface. CNI
// plugins set Sandbox on interfaces inside a sandbox (the container side);
// host-side veth/bridge entries carry an empty Sandbox.
func containerInterfaceMAC(result *types100.Result, preferred string) string {
	if mac := interfaceMACByIPConfig(result); mac != "" {
		return mac
	}
	if mac := sandboxInterfaceMAC(result.Interfaces, preferred); mac != "" {
		return mac
	}
	return namedInterfaceMAC(result.Interfaces, preferred)
}

// interfaceMACByIPConfig resolves the MAC of the interface referenced by the
// first IP config that points at a MAC-carrying result interface.
func interfaceMACByIPConfig(result *types100.Result) string {
	for _, ipConfig := range result.IPs {
		if ipConfig == nil || ipConfig.Interface == nil {
			continue
		}
		index := *ipConfig.Interface
		if index < 0 || index >= len(result.Interfaces) {
			continue
		}
		if intf := result.Interfaces[index]; intf != nil && intf.Mac != "" {
			return intf.Mac
		}
	}
	return ""
}

// sandboxInterfaceMAC prefers the sandbox interface named preferred, falling
// back to the first sandbox MAC seen in result order.
func sandboxInterfaceMAC(interfaces []*types100.Interface, preferred string) string {
	fallback := ""
	for _, intf := range interfaces {
		if intf == nil || intf.Mac == "" || intf.Sandbox == "" {
			continue
		}
		if intf.Name == preferred {
			return intf.Mac
		}
		if fallback == "" {
			fallback = intf.Mac
		}
	}
	return fallback
}

// namedInterfaceMAC returns the MAC of the first MAC-carrying interface named
// preferred, host- or container-side.
func namedInterfaceMAC(interfaces []*types100.Interface, preferred string) string {
	for _, intf := range interfaces {
		if intf != nil && intf.Mac != "" && intf.Name == preferred {
			return intf.Mac
		}
	}
	return ""
}

// PublishedPorts converts concrete bindings into Docker's PortMap shape. It
// rejects any binding that still carries Docker's host port 0.
func PublishedPorts(bindings []domain.PortBinding) (mobynetwork.PortMap, error) {
	ports := make(mobynetwork.PortMap, len(bindings))
	for _, binding := range bindings {
		if binding.HostPort == 0 {
			return nil, fmt.Errorf("container port %d/%s: %w", binding.ContainerPort, binding.Protocol, ErrUnallocatedPort)
		}
		protocol, err := normalizeProtocol(binding.Protocol)
		if err != nil {
			return nil, err
		}
		port, ok := mobynetwork.PortFrom(binding.ContainerPort, mobynetwork.IPProtocol(protocol))
		if !ok {
			return nil, fmt.Errorf("cni: invalid container port %d/%s", binding.ContainerPort, protocol)
		}
		hostIP, err := netip.ParseAddr(normalizeHostIP(binding.HostIP))
		if err != nil {
			return nil, fmt.Errorf("cni: invalid host ip %q: %w", binding.HostIP, err)
		}
		ports[port] = append(ports[port], mobynetwork.PortBinding{
			HostIP:   hostIP,
			HostPort: strconv.Itoa(int(binding.HostPort)),
		})
	}
	return ports, nil
}

// SynthesizeSettings builds Docker's inspect NetworkSettings from the
// container's attachments and concrete port bindings.
func SynthesizeSettings(attachments []domain.NetworkAttachment, ports []domain.PortBinding) (*container.NetworkSettings, error) {
	portMap, err := PublishedPorts(ports)
	if err != nil {
		return nil, err
	}
	settings := &container.NetworkSettings{
		Ports:    portMap,
		Networks: make(map[string]*mobynetwork.EndpointSettings, len(attachments)),
	}
	for _, attachment := range attachments {
		endpoint, err := endpointSettings(attachment)
		if err != nil {
			return nil, err
		}
		settings.Networks[attachment.Name] = endpoint
	}
	return settings, nil
}

func endpointSettings(attachment domain.NetworkAttachment) (*mobynetwork.EndpointSettings, error) {
	endpoint := &mobynetwork.EndpointSettings{
		NetworkID:           string(attachment.NetworkID),
		Aliases:             attachment.Aliases,
		IPPrefixLen:         attachment.IPPrefixLen,
		GlobalIPv6PrefixLen: attachment.GlobalIPv6PrefixLen,
	}
	for _, field := range []struct {
		source string
		target *netip.Addr
	}{
		{attachment.IPAddress, &endpoint.IPAddress},
		{attachment.Gateway, &endpoint.Gateway},
		{attachment.GlobalIPv6Address, &endpoint.GlobalIPv6Address},
	} {
		if field.source == "" {
			continue
		}
		addr, err := netip.ParseAddr(field.source)
		if err != nil {
			return nil, fmt.Errorf("cni: endpoint address %q: %w", field.source, err)
		}
		*field.target = addr
	}
	if attachment.MACAddress != "" {
		hardware, err := net.ParseMAC(attachment.MACAddress)
		if err != nil {
			return nil, fmt.Errorf("cni: endpoint mac %q: %w", attachment.MACAddress, err)
		}
		endpoint.MacAddress = mobynetwork.HardwareAddr(hardware)
	}
	return endpoint, nil
}

// FillFromNetNS overlays live netns state for fields the CNI result did not
// carry; existing values are never overwritten.
func FillFromNetNS(attachment *domain.NetworkAttachment, probe NetNSProbe, netnsPath, ifName string) error {
	if attachment == nil || probe == nil || netnsPath == "" || ifName == "" {
		return nil
	}
	info, err := probe.InterfaceInfo(netnsPath, ifName)
	if err != nil {
		return err
	}
	if attachment.MACAddress == "" {
		attachment.MACAddress = info.MAC
	}
	for _, ipNet := range info.IPAddresses {
		if ipNet.IP == nil {
			continue
		}
		if ipNet.IP.To4() != nil {
			if attachment.IPAddress == "" {
				attachment.IPAddress = ipNet.IP.String()
				attachment.IPPrefixLen, _ = ipNet.Mask.Size()
			}
			continue
		}
		if attachment.GlobalIPv6Address == "" && !ipNet.IP.IsLinkLocalUnicast() {
			attachment.GlobalIPv6Address = ipNet.IP.String()
			attachment.GlobalIPv6PrefixLen, _ = ipNet.Mask.Size()
		}
	}
	return nil
}
