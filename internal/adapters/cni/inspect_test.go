package cni_test

import (
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/moby/moby/api/types/container"

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
	assertSynthesizeSettingsRejectsUnallocated(t, attachment)

	settings, err := cni.SynthesizeSettings([]domain.NetworkAttachment{attachment}, []domain.PortBinding{
		{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 49153},
	})
	if err != nil {
		t.Fatalf("SynthesizeSettings: %v", err)
	}
	assertPublishedSettings(t, settings)
}

// settingsDocument mirrors the JSON shape consumers see, so the assertions run
// against the serialized form instead of the Go struct.
type settingsDocument struct {
	Ports    map[string][]publishedBinding `json:"Ports"`
	Networks map[string]publishedEndpoint  `json:"Networks"`
}

type publishedBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type publishedEndpoint struct {
	IPAddress  string
	Gateway    string
	MacAddress string
	Aliases    []string
	NetworkID  string
}

func assertSynthesizeSettingsRejectsUnallocated(t *testing.T, attachment domain.NetworkAttachment) {
	t.Helper()
	_, err := cni.SynthesizeSettings([]domain.NetworkAttachment{attachment}, []domain.PortBinding{
		{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 0},
	})
	if !errors.Is(err, cni.ErrUnallocatedPort) {
		t.Fatalf("SynthesizeSettings(host port 0) = %v, want ErrUnallocatedPort", err)
	}
}

func assertPublishedSettings(t *testing.T, settings *container.NetworkSettings) {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var document settingsDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal settings %s: %v", raw, err)
	}
	assertPublishedBindings(t, document.Ports)
	assertPublishedEndpoint(t, document.Networks)
}

func assertPublishedBindings(t *testing.T, ports map[string][]publishedBinding) {
	t.Helper()
	bindings, ok := ports["80/tcp"]
	if !ok || len(bindings) != 1 {
		t.Fatalf("Ports = %v, want 80/tcp entry", ports)
	}
	if bindings[0].HostPort != "49153" || bindings[0].HostPort == "0" {
		t.Fatalf("published HostPort = %q, want nonzero 49153", bindings[0].HostPort)
	}
	if bindings[0].HostIP != "0.0.0.0" {
		t.Fatalf("HostIp = %q", bindings[0].HostIP)
	}
}

func assertPublishedEndpoint(t *testing.T, networks map[string]publishedEndpoint) {
	t.Helper()
	endpoint, ok := networks["web"]
	if !ok {
		t.Fatalf("Networks = %v, want web", networks)
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
