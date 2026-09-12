package cni_test

import (
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
)

type fakeProbe struct {
	info cni.InterfaceInfo
	err  error
}

func (p fakeProbe) InterfaceInfo(string, string) (cni.InterfaceInfo, error) {
	return p.info, p.err
}

func TestAttachmentFromCNIResultPrefersContainerInterface(t *testing.T) {
	attachment, err := cni.AttachmentFromResult("net-1", "web", "eth0", []string{"alias"}, sampleResult())
	if err != nil {
		t.Fatalf("AttachmentFromResult: %v", err)
	}
	if attachment.IPAddress != "10.88.0.5" || attachment.IPPrefixLen != 24 {
		t.Fatalf("ip = %s/%d", attachment.IPAddress, attachment.IPPrefixLen)
	}
	if attachment.Gateway != "10.88.0.1" {
		t.Fatalf("gateway = %q", attachment.Gateway)
	}
	if attachment.MACAddress != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("mac = %q, want container-side mac", attachment.MACAddress)
	}
	if attachment.NetworkID != "net-1" || attachment.Name != "web" {
		t.Fatalf("identity = %+v", attachment)
	}
	if len(attachment.Aliases) != 1 || attachment.Aliases[0] != "alias" {
		t.Fatalf("aliases = %v", attachment.Aliases)
	}
}

func TestAttachmentFromCNIResultRejectsNil(t *testing.T) {
	if _, err := cni.AttachmentFromResult("net-1", "web", "eth0", nil, nil); !errors.Is(err, cni.ErrNilResult) {
		t.Fatalf("nil result = %v, want ErrNilResult", err)
	}
}

func TestSynthesizeSettingsPublishesNonzeroPorts(t *testing.T) {
	attachment := domain.NetworkAttachment{
		NetworkID:   "net-1",
		Name:        "web",
		IPAddress:   "10.88.0.5",
		IPPrefixLen: 24,
		Gateway:     "10.88.0.1",
		MACAddress:  "aa:bb:cc:dd:ee:02",
		Aliases:     []string{"web-alias"},
	}
	settings, err := cni.SynthesizeSettings([]domain.NetworkAttachment{attachment}, []domain.PortBinding{
		{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 0},
	})
	if !errors.Is(err, cni.ErrUnallocatedPort) {
		t.Fatalf("SynthesizeSettings(host port 0) = %v, want ErrUnallocatedPort", err)
	}

	settings, err = cni.SynthesizeSettings([]domain.NetworkAttachment{attachment}, []domain.PortBinding{
		{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 49153},
	})
	if err != nil {
		t.Fatalf("SynthesizeSettings: %v", err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var document struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]struct {
			IPAddress  string
			Gateway    string
			MacAddress string
			Aliases    []string
			NetworkID  string
		} `json:"Networks"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal settings %s: %v", raw, err)
	}
	bindings, ok := document.Ports["80/tcp"]
	if !ok || len(bindings) != 1 {
		t.Fatalf("Ports = %v, want 80/tcp entry", document.Ports)
	}
	if bindings[0].HostPort != "49153" || bindings[0].HostPort == "0" {
		t.Fatalf("published HostPort = %q, want nonzero 49153", bindings[0].HostPort)
	}
	if bindings[0].HostIP != "0.0.0.0" {
		t.Fatalf("HostIp = %q", bindings[0].HostIP)
	}
	endpoint, ok := document.Networks["web"]
	if !ok {
		t.Fatalf("Networks = %v, want web", document.Networks)
	}
	if endpoint.IPAddress != "10.88.0.5" || endpoint.Gateway != "10.88.0.1" {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	if endpoint.MacAddress != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("endpoint mac = %q", endpoint.MacAddress)
	}
	if endpoint.NetworkID != "net-1" {
		t.Fatalf("endpoint network id = %q", endpoint.NetworkID)
	}
}

func TestFillFromNetNSFillsOnlyMissingFields(t *testing.T) {
	probe := fakeProbe{info: cni.InterfaceInfo{
		MAC: "02:42:ac:11:00:02",
		IPAddresses: []net.IPNet{
			{IP: net.ParseIP("10.88.0.77"), Mask: net.CIDRMask(24, 32)},
			{IP: net.ParseIP("fd00::77"), Mask: net.CIDRMask(64, 128)},
		},
	}}
	attachment := domain.NetworkAttachment{IPAddress: "10.88.0.9", IPPrefixLen: 24}
	if err := cni.FillFromNetNS(&attachment, probe, "/var/run/netns/c1", "eth0"); err != nil {
		t.Fatalf("FillFromNetNS: %v", err)
	}
	if attachment.IPAddress != "10.88.0.9" {
		t.Fatalf("existing ip was overwritten: %q", attachment.IPAddress)
	}
	if attachment.MACAddress != "02:42:ac:11:00:02" {
		t.Fatalf("mac = %q", attachment.MACAddress)
	}
	if attachment.GlobalIPv6Address != "fd00::77" || attachment.GlobalIPv6PrefixLen != 64 {
		t.Fatalf("ipv6 = %s/%d", attachment.GlobalIPv6Address, attachment.GlobalIPv6PrefixLen)
	}

	empty := domain.NetworkAttachment{}
	if err := cni.FillFromNetNS(&empty, probe, "/var/run/netns/c1", "eth0"); err != nil {
		t.Fatalf("FillFromNetNS(empty): %v", err)
	}
	if empty.IPAddress != "10.88.0.77" || empty.IPPrefixLen != 24 {
		t.Fatalf("filled ip = %s/%d", empty.IPAddress, empty.IPPrefixLen)
	}
}

func TestFillFromNetNSIsNoopWithoutProbe(t *testing.T) {
	attachment := domain.NetworkAttachment{}
	if err := cni.FillFromNetNS(&attachment, nil, "/var/run/netns/c1", "eth0"); err != nil {
		t.Fatalf("nil probe: %v", err)
	}
	if attachment.IPAddress != "" {
		t.Fatalf("no-op probe mutated attachment: %+v", attachment)
	}
}
