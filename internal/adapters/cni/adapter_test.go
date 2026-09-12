package cni_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	types100 "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"

	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
)

var errFakeUnsupported = errors.New("fakeCNI: unsupported method")

type fakeCNI struct {
	addCalls       int
	delCalls       int
	lastList       *libcni.NetworkConfigList
	lastRuntime    *libcni.RuntimeConf
	lastDelRuntime *libcni.RuntimeConf
	cachedConfig   []byte
	cachedRuntime  *libcni.RuntimeConf
	addResult      types.Result
	addErr         error
	delErr         error
	cached         []*libcni.NetworkAttachment
}

func (f *fakeCNI) AddNetworkList(_ context.Context, list *libcni.NetworkConfigList, rt *libcni.RuntimeConf) (types.Result, error) {
	f.addCalls++
	f.lastList = list
	f.lastRuntime = rt
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.cachedConfig = append([]byte(nil), list.Bytes...)
	cached := *rt
	f.cachedRuntime = &cached
	if f.addResult != nil {
		return f.addResult, nil
	}
	return &types100.Result{CNIVersion: "1.0.0"}, nil
}

func (f *fakeCNI) DelNetworkList(_ context.Context, _ *libcni.NetworkConfigList, rt *libcni.RuntimeConf) error {
	f.delCalls++
	f.lastDelRuntime = rt
	if f.delErr != nil {
		return f.delErr
	}
	f.cachedConfig = nil
	f.cachedRuntime = nil
	return nil
}

func (f *fakeCNI) GetCachedAttachments(string) ([]*libcni.NetworkAttachment, error) {
	return f.cached, nil
}

func (f *fakeCNI) CheckNetworkList(context.Context, *libcni.NetworkConfigList, *libcni.RuntimeConf) error {
	return nil
}

func (f *fakeCNI) GetNetworkListCachedResult(*libcni.NetworkConfigList, *libcni.RuntimeConf) (types.Result, error) {
	return nil, errFakeUnsupported
}

func (f *fakeCNI) GetNetworkListCachedConfig(*libcni.NetworkConfigList, *libcni.RuntimeConf) ([]byte, *libcni.RuntimeConf, error) {
	return f.cachedConfig, f.cachedRuntime, nil
}

func (f *fakeCNI) AddNetwork(context.Context, *libcni.PluginConfig, *libcni.RuntimeConf) (types.Result, error) {
	return nil, errFakeUnsupported
}

func (f *fakeCNI) CheckNetwork(context.Context, *libcni.PluginConfig, *libcni.RuntimeConf) error {
	return nil
}

func (f *fakeCNI) DelNetwork(context.Context, *libcni.PluginConfig, *libcni.RuntimeConf) error {
	return nil
}

func (f *fakeCNI) GetNetworkCachedResult(*libcni.PluginConfig, *libcni.RuntimeConf) (types.Result, error) {
	return nil, errFakeUnsupported
}

func (f *fakeCNI) GetNetworkCachedConfig(*libcni.PluginConfig, *libcni.RuntimeConf) ([]byte, *libcni.RuntimeConf, error) {
	return nil, nil, errFakeUnsupported
}

func (f *fakeCNI) ValidateNetworkList(context.Context, *libcni.NetworkConfigList) ([]string, error) {
	return nil, nil
}

func (f *fakeCNI) ValidateNetwork(context.Context, *libcni.PluginConfig) ([]string, error) {
	return nil, nil
}

func (f *fakeCNI) GCNetworkList(context.Context, *libcni.NetworkConfigList, *libcni.GCArgs) error {
	return nil
}

func (f *fakeCNI) GetStatusNetworkList(context.Context, *libcni.NetworkConfigList) error {
	return nil
}

func (f *fakeCNI) GetVersionInfo(context.Context, string) (version.PluginInfo, error) {
	return nil, errFakeUnsupported
}

var _ libcni.CNI = (*fakeCNI)(nil)

func sampleResult() types.Result {
	containerIfIndex := 1
	return &types100.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*types100.Interface{
			{Name: "veth1234", Mac: "aa:bb:cc:dd:ee:01"},
			{Name: "eth0", Mac: "aa:bb:cc:dd:ee:02", Sandbox: "/var/run/netns/c1"},
		},
		IPs: []*types100.IPConfig{{
			Interface: &containerIfIndex,
			Address:   net.IPNet{IP: net.ParseIP("10.88.0.5").To4(), Mask: net.CIDRMask(24, 32)},
			Gateway:   net.ParseIP("10.88.0.1"),
		}},
	}
}

func fixedPortProbe(dynamic uint16) cni.PortAllocatorOption {
	return cni.WithPortProbe(func(_, _ string, requested uint16) (uint16, error) {
		if requested != 0 {
			return requested, nil
		}
		return dynamic, nil
	})
}

func newTestAdapter(t *testing.T) (*cni.Adapter, *fakeCNI, string) {
	t.Helper()
	dir := t.TempDir()
	fake := &fakeCNI{addResult: sampleResult()}
	cfg := cni.Config{
		NetConfDir: dir,
		CNI:        fake,
		Allocator:  cni.NewPortAllocator(fixedPortProbe(54000)),
	}
	return cni.New(cfg), fake, dir
}

func TestCreateListResolveRemoveNetwork(t *testing.T) {
	adapter, _, dir := newTestAdapter(t)
	ctx := context.Background()

	id, err := adapter.Create(ctx, "web")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned an empty network id")
	}
	file := filepath.Join(dir, "dockerdless-web.conflist")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("conflist missing: %v", err)
	}
	if _, err := adapter.Create(ctx, "web"); !errors.Is(err, cni.ErrNetworkExists) {
		t.Fatalf("duplicate Create = %v, want ErrNetworkExists", err)
	}

	list, err := adapter.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("List = %+v, want one web network", list)
	}
	if list[0].Subnet != "10.88.0.0/24" || list[0].Gateway != "10.88.0.1" {
		t.Fatalf("subnet/gateway = %s/%s, want 10.88.0.0/24 and 10.88.0.1", list[0].Subnet, list[0].Gateway)
	}
	if list[0].Bridge == "" || list[0].Bridge[:3] != "dls" {
		t.Fatalf("bridge = %q, want dls*", list[0].Bridge)
	}

	byID, err := adapter.Resolve(ctx, string(id))
	if err != nil || byID.Name != "web" {
		t.Fatalf("Resolve(id) = %+v, %v", byID, err)
	}
	byPrefix, err := adapter.Resolve(ctx, string(id)[:12])
	if err != nil || byPrefix.Name != "web" {
		t.Fatalf("Resolve(prefix) = %+v, %v", byPrefix, err)
	}

	if err := adapter.Remove(ctx, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflist still present after Remove: %v", err)
	}
	if err := adapter.Remove(ctx, id); !errors.Is(err, cni.ErrNetworkNotFound) {
		t.Fatalf("second Remove = %v, want ErrNetworkNotFound", err)
	}
}

func TestCreateNetworkValidations(t *testing.T) {
	tests := []struct {
		name string
		req  cni.CreateNetworkRequest
		want error
	}{
		{"empty name", cni.CreateNetworkRequest{}, cni.ErrInvalidNetworkName},
		{"bad characters", cni.CreateNetworkRequest{Name: "bad name"}, cni.ErrInvalidNetworkName},
		{"reserved host", cni.CreateNetworkRequest{Name: "host"}, cni.ErrReservedNetworkName},
		{"reserved none", cni.CreateNetworkRequest{Name: "none"}, cni.ErrReservedNetworkName},
		{"unsupported driver", cni.CreateNetworkRequest{Name: "net", Driver: "overlay"}, cni.ErrUnsupportedNetworkDriver},
		{"ipv6", cni.CreateNetworkRequest{Name: "net", EnableIPv6: true}, cni.ErrIPv6Unsupported},
		{"bad subnet", cni.CreateNetworkRequest{Name: "net", Subnet: "not-a-subnet"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, _, _ := newTestAdapter(t)
			_, err := adapter.CreateNetwork(context.Background(), tt.req)
			if err == nil {
				t.Fatalf("CreateNetwork(%+v) succeeded, want error", tt.req)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("CreateNetwork error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCreateNetworksGetDistinctSubnets(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "alpha"); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	id, err := adapter.Create(ctx, "beta")
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	beta, err := adapter.Resolve(ctx, string(id))
	if err != nil {
		t.Fatalf("resolve beta: %v", err)
	}
	if beta.Subnet != "10.88.1.0/24" {
		t.Fatalf("beta subnet = %q, want 10.88.1.0/24", beta.Subnet)
	}
}

func TestEnsureDefaultNetworkIsIdempotent(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	ctx := context.Background()
	first, err := adapter.EnsureDefaultNetwork(ctx)
	if err != nil {
		t.Fatalf("EnsureDefaultNetwork: %v", err)
	}
	second, err := adapter.EnsureDefaultNetwork(ctx)
	if err != nil {
		t.Fatalf("EnsureDefaultNetwork second: %v", err)
	}
	if first != second {
		t.Fatalf("ids differ: %q vs %q", first, second)
	}
}

func TestRemoveNetworkRejectsActiveEndpoints(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	id, err := adapter.Create(ctx, "web")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake.cached = []*libcni.NetworkAttachment{{ContainerID: "c1", Network: "web"}}
	if err := adapter.Remove(ctx, id); !errors.Is(err, cni.ErrNetworkInUse) {
		t.Fatalf("Remove with endpoints = %v, want ErrNetworkInUse", err)
	}
	fake.cached = nil
	if err := adapter.Remove(ctx, id); err != nil {
		t.Fatalf("Remove after endpoints gone: %v", err)
	}
}

func TestConnectAllocatesPortsBeforeCNI(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network:   "web",
		Container: "container-1",
		NetNS:     "/var/run/netns/c1",
		Ports: []domain.PortBinding{
			{ContainerPort: 80, Protocol: "tcp"},
			{ContainerPort: 53, Protocol: "udp"},
		},
		Aliases: []string{"web-alias"},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if fake.addCalls != 1 {
		t.Fatalf("addCalls = %d, want 1", fake.addCalls)
	}
	if fake.lastRuntime.NetNS != "/var/run/netns/c1" || fake.lastRuntime.IfName != "eth0" {
		t.Fatalf("runtime conf = %+v", fake.lastRuntime)
	}
	rawMappings, ok := fake.lastRuntime.CapabilityArgs["portMappings"].([]cni.PortMapping)
	if !ok {
		t.Fatalf("portMappings capability missing: %#v", fake.lastRuntime.CapabilityArgs)
	}
	if len(rawMappings) != 2 {
		t.Fatalf("len(portMappings) = %d, want 2", len(rawMappings))
	}
	for _, mapping := range rawMappings {
		if mapping.HostPort == 0 {
			t.Fatalf("CNI portmap received host port 0: %+v", mapping)
		}
	}
	for _, binding := range result.Ports {
		if binding.HostPort == 0 {
			t.Fatalf("published binding has host port 0: %+v", binding)
		}
	}
	attachment := result.Attachment
	if attachment.IPAddress != "10.88.0.5" || attachment.Gateway != "10.88.0.1" {
		t.Fatalf("attachment ip/gw = %s/%s", attachment.IPAddress, attachment.Gateway)
	}
	if attachment.MACAddress != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("attachment mac = %q, want container-side mac", attachment.MACAddress)
	}
	if attachment.Name != "web" || len(attachment.Aliases) != 1 || attachment.Aliases[0] != "web-alias" {
		t.Fatalf("attachment = %+v", attachment)
	}
}

func TestConnectFixedPortCollisionAcrossContainers(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	port := []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 8080}}
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c1", NetNS: "/ns/c1", Ports: port}); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	_, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c2", NetNS: "/ns/c2", Ports: port})
	if !errors.Is(err, cni.ErrPortInUse) {
		t.Fatalf("colliding Connect = %v, want ErrPortInUse", err)
	}
	if fake.addCalls != 1 {
		t.Fatalf("addCalls = %d, want 1 (failed allocation must not reach CNI)", fake.addCalls)
	}
}

func TestConnectRollsBackPortsOnCNIAddFailure(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake.addErr = errors.New("bridge plugin exploded")
	_, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network: "web", Container: "c1", NetNS: "/ns/c1",
		Ports: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp"}},
	})
	if err == nil || !errors.Is(err, fake.addErr) {
		t.Fatalf("Connect with failing CNI = %v", err)
	}
	fake.addErr = nil
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network: "web", Container: "c2", NetNS: "/ns/c2",
		Ports: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 54000}},
	}); err != nil {
		t.Fatalf("dynamic port 54000 leaked after failed add: %v", err)
	}
}

func TestConnectHostAndNoneModesSkipCNI(t *testing.T) {
	for _, mode := range []string{"host", "none"} {
		t.Run(mode, func(t *testing.T) {
			adapter, fake, _ := newTestAdapter(t)
			ctx := context.Background()
			result, err := adapter.Connect(ctx, cni.ConnectRequest{Network: mode, Container: "c1"})
			if err != nil {
				t.Fatalf("Connect(%s): %v", mode, err)
			}
			if result.Attachment.Name != mode {
				t.Fatalf("attachment name = %q, want %q", result.Attachment.Name, mode)
			}
			if fake.addCalls != 0 {
				t.Fatalf("addCalls = %d, want 0 for %s mode", fake.addCalls, mode)
			}
			_, err = adapter.Connect(ctx, cni.ConnectRequest{
				Network: mode, Container: "c2",
				Ports: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp"}},
			})
			if !errors.Is(err, cni.ErrPortPublishWithMode) {
				t.Fatalf("Connect(%s) with ports = %v, want ErrPortPublishWithMode", mode, err)
			}
		})
	}
}

func TestParseNetworkMode(t *testing.T) {
	tests := []struct {
		mode string
		want cni.Mode
		err  bool
	}{
		{"", cni.ModeNetwork, false},
		{"default", cni.ModeNetwork, false},
		{"bridge", cni.ModeNetwork, false},
		{"host", cni.ModeHost, false},
		{"HOST", cni.ModeHost, false},
		{"none", cni.ModeNone, false},
		{"mynet", cni.ModeNetwork, false},
		{"container:abc", "", true},
		{"ns:/proc/1/ns/net", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			mode, err := cni.ParseNetworkMode(tt.mode)
			if tt.err {
				if !errors.Is(err, cni.ErrUnknownNetworkMode) {
					t.Fatalf("ParseNetworkMode(%q) = %v, want ErrUnknownNetworkMode", tt.mode, err)
				}
				return
			}
			if err != nil || mode != tt.want {
				t.Fatalf("ParseNetworkMode(%q) = %v, %v; want %v", tt.mode, mode, err, tt.want)
			}
		})
	}
}

func TestConnectUnknownNetworkFails(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	_, err := adapter.Connect(context.Background(), cni.ConnectRequest{Network: "missing", Container: "c1", NetNS: "/ns/c1"})
	if !errors.Is(err, cni.ErrNetworkNotFound) {
		t.Fatalf("Connect(unknown) = %v, want ErrNetworkNotFound", err)
	}
	if fake.addCalls != 0 {
		t.Fatalf("addCalls = %d, want 0", fake.addCalls)
	}
}

func TestDisconnectReleasesPortsForReuse(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	port := []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 9090}}
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c1", NetNS: "/ns/c1", Ports: port}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := adapter.Disconnect(ctx, cni.DisconnectRequest{Network: "web", Container: "c1", NetNS: "/ns/c1"}); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if fake.delCalls != 1 {
		t.Fatalf("delCalls = %d, want 1", fake.delCalls)
	}
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c2", NetNS: "/ns/c2", Ports: port}); err != nil {
		t.Fatalf("port 9090 not released after disconnect: %v", err)
	}
}

func TestDisconnectCNIFailureReleasesPorts(t *testing.T) {
	adapter, fake, _ := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	port := []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 9191}}
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c1", NetNS: "/ns/c1", Ports: port}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	fake.delErr = errors.New("firewall plugin exploded")
	err := adapter.Disconnect(ctx, cni.DisconnectRequest{Network: "web", Container: "c1", NetNS: "/ns/c1"})
	if err == nil || !errors.Is(err, fake.delErr) {
		t.Fatalf("Disconnect = %v, want wrapped del error", err)
	}
	fake.delErr = nil
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: "web", Container: "c2", NetNS: "/ns/c2", Ports: port}); err != nil {
		t.Fatalf("port 9191 leaked after failed CNI teardown: %v", err)
	}
}

func TestDisconnectReplaysPortMappingsForPortmapPlugin(t *testing.T) {
	adapter, fake, dir := newTestAdapter(t)
	ctx := context.Background()
	if _, err := adapter.Create(ctx, "web"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network: "web", Container: "c1", NetNS: "/ns/c1",
		Ports: []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp"}},
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	fresh := cni.New(cni.Config{
		NetConfDir: dir,
		CNI:        fake,
		Allocator:  cni.NewPortAllocator(fixedPortProbe(54000)),
	})
	if err := fresh.Disconnect(ctx, cni.DisconnectRequest{Network: "web", Container: "c1"}); err != nil {
		t.Fatalf("Disconnect via fresh adapter: %v", err)
	}
	raw, ok := fake.lastDelRuntime.CapabilityArgs["portMappings"].([]cni.PortMapping)
	if !ok || len(raw) != 1 {
		t.Fatalf("DEL runtime portMappings = %#v, want one mapping", fake.lastDelRuntime.CapabilityArgs)
	}
	if raw[0].HostPort != 54000 || raw[0].ContainerPort != 80 {
		t.Fatalf("DEL mapping = %+v, want 80 -> 54000", raw[0])
	}
	if fake.lastDelRuntime.NetNS != "/ns/c1" {
		t.Fatalf("DEL netns = %q, want cached netns", fake.lastDelRuntime.NetNS)
	}
}

func TestDisconnectAllReleasesEveryAttachment(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)
	ctx := context.Background()
	for _, name := range []string{"web", "db"} {
		if _, err := adapter.Create(ctx, name); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		if _, err := adapter.Connect(ctx, cni.ConnectRequest{Network: name, Container: "c1", NetNS: "/ns/c1"}); err != nil {
			t.Fatalf("Connect(%s): %v", name, err)
		}
	}
	if errs := adapter.DisconnectAll(ctx, "c1"); len(errs) != 0 {
		t.Fatalf("DisconnectAll errors: %v", errs)
	}
	network, err := adapter.Resolve(ctx, "web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := adapter.Remove(ctx, network.ID); err != nil {
		t.Fatalf("Remove after DisconnectAll: %v", err)
	}
}
