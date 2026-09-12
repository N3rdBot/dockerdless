//go:build cniintegration

package cni_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
)

const (
	integrationSubnet   = "10.213.7.0/24"
	integrationGateway  = "10.213.7.1"
	integrationPlugin   = "/opt/cni/bin"
	integrationNetnsDir = "/var/run/netns"
)

func requireCNIEnvironment(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("live CNI integration requires root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 (ip) is not installed")
	}
	for _, plugin := range []string{"bridge", "host-local", "portmap"} {
		if _, err := os.Stat(filepath.Join(integrationPlugin, plugin)); err != nil {
			t.Skipf("CNI plugin %q is not installed", plugin)
		}
	}
	probe := fmt.Sprintf("dls-probe-%d", os.Getpid())
	if out, err := exec.Command("ip", "netns", "add", probe).CombinedOutput(); err != nil {
		t.Skipf("cannot create network namespaces: %v (%s)", err, out)
	}
	_ = exec.Command("ip", "netns", "del", probe).Run()
	if hostRouteExists(t, integrationSubnet) {
		t.Skipf("subnet %s is already routed on the host", integrationSubnet)
	}
}

func hostRouteExists(t *testing.T, subnet string) bool {
	t.Helper()
	out, err := exec.Command("ip", "-j", "route", "show").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strings.TrimSuffix(subnet, "/24"))
}

func runCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func commandFails(t *testing.T, name string, args ...string) bool {
	t.Helper()
	err := exec.Command(name, args...).Run()
	return err != nil
}

func TestLiveCNIBridgeLifecycle(t *testing.T) {
	requireCNIEnvironment(t)
	ctx := context.Background()

	nsName := fmt.Sprintf("dls-it-%d", os.Getpid())
	runCommand(t, "ip", "netns", "add", nsName)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", nsName).Run() })
	netnsPath := filepath.Join(integrationNetnsDir, nsName)

	adapter := cni.New(cni.Config{
		NetConfDir:  t.TempDir(),
		PluginDirs:  []string{integrationPlugin},
		CacheDir:    t.TempDir(),
		IPAMDataDir: filepath.Join(t.TempDir(), "ipam"),
	})

	var connectedContainers []domain.ContainerID

	id, err := adapter.CreateNetwork(ctx, cni.CreateNetworkRequest{
		Name:    "itnet",
		Subnet:  integrationSubnet,
		Gateway: integrationGateway,
	})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	t.Cleanup(func() {
		for _, container := range connectedContainers {
			_ = adapter.Disconnect(context.Background(), cni.DisconnectRequest{
				Network:   string(id),
				Container: container,
				NetNS:     netnsPath,
			})
		}
	})
	network, err := adapter.Resolve(ctx, string(id))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	bridgeName := network.Bridge
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", bridgeName).Run() })

	result, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network:   string(id),
		Container: "it-container",
		NetNS:     netnsPath,
		Ports:     []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 0}},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	connectedContainers = append(connectedContainers, "it-container")
	attachment := result.Attachment
	if !strings.HasPrefix(attachment.IPAddress, "10.213.7.") {
		t.Fatalf("attachment ip = %q, want an address in %s", attachment.IPAddress, integrationSubnet)
	}
	if attachment.Gateway != integrationGateway {
		t.Fatalf("attachment gateway = %q, want %s", attachment.Gateway, integrationGateway)
	}
	if len(result.Ports) != 1 || result.Ports[0].HostPort == 0 {
		t.Fatalf("published ports = %+v, want one nonzero host port", result.Ports)
	}
	publishedPort := result.Ports[0].HostPort
	t.Logf("ATTACHMENT ip=%s gw=%s mac=%s published=%d/tcp", attachment.IPAddress, attachment.Gateway, attachment.MACAddress, publishedPort)

	checkNetnsAddress(t, nsName, attachment.IPAddress, attachment.MACAddress)
	checkPortmapRule(t, publishedPort)
	checkVethMasterCount(t, bridgeName, 1)

	if err := adapter.Disconnect(ctx, cni.DisconnectRequest{
		Network:   string(id),
		Container: "it-container",
		NetNS:     netnsPath,
	}); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if !commandFails(t, "ip", "netns", "exec", nsName, "ip", "link", "show", "eth0") {
		t.Fatal("container eth0 still exists after Disconnect")
	}
	checkVethMasterCount(t, bridgeName, 0)

	reuse, err := adapter.Connect(ctx, cni.ConnectRequest{
		Network:   string(id),
		Container: "it-container-2",
		NetNS:     netnsPath,
		Ports:     []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: publishedPort}},
	})
	if err != nil {
		t.Fatalf("reusing released port %d after Disconnect failed: %v", publishedPort, err)
	}
	connectedContainers = append(connectedContainers, "it-container-2")
	if reuse.Ports[0].HostPort != publishedPort {
		t.Fatalf("reuse port = %d, want %d", reuse.Ports[0].HostPort, publishedPort)
	}
	if err := adapter.Disconnect(ctx, cni.DisconnectRequest{
		Network:   string(id),
		Container: "it-container-2",
		NetNS:     netnsPath,
	}); err != nil {
		t.Fatalf("Disconnect second: %v", err)
	}

	if err := adapter.Remove(ctx, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !commandFails(t, "ip", "link", "show", bridgeName) {
		t.Fatalf("bridge %s still exists after Remove", bridgeName)
	}
	if _, err := os.Stat(network.File); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflist still present after Remove: %v", err)
	}
}

func checkNetnsAddress(t *testing.T, nsName, wantIP, wantMAC string) {
	t.Helper()
	out := runCommand(t, "ip", "-j", "netns", "exec", nsName, "ip", "-j", "addr", "show", "eth0")
	var links []struct {
		Address  string `json:"address"`
		AddrInfo []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		t.Fatalf("parsing netns addr json %q: %v", out, err)
	}
	if len(links) != 1 {
		t.Fatalf("netns links = %d, want 1", len(links))
	}
	if links[0].Address != wantMAC {
		t.Fatalf("netns mac = %q, want %q", links[0].Address, wantMAC)
	}
	found := false
	for _, addr := range links[0].AddrInfo {
		if addr.Local == wantIP {
			found = true
		}
	}
	if !found {
		t.Fatalf("netns addresses %+v do not include %s", links[0].AddrInfo, wantIP)
	}
}

func checkPortmapRule(t *testing.T, hostPort uint16) {
	t.Helper()
	out, err := exec.Command("iptables", "-t", "nat", "-S").CombinedOutput()
	if err != nil {
		t.Skipf("iptables not usable for portmap verification: %v (%s)", err, out)
	}
	if !bytes.Contains(out, []byte(fmt.Sprintf("--dport %d", hostPort))) {
		t.Fatalf("no iptables rule publishes dport %d:\n%s", hostPort, out)
	}
}

func checkVethMasterCount(t *testing.T, bridgeName string, want int) {
	t.Helper()
	out, err := exec.Command("ip", "-j", "link", "show", "master", bridgeName).Output()
	if err != nil {
		if want == 0 {
			return
		}
		t.Fatalf("listing veths on %s: %v", bridgeName, err)
	}
	var links []struct {
		IfName string `json:"ifname"`
	}
	if err := json.Unmarshal(out, &links); err != nil {
		t.Fatalf("parsing link json %q: %v", out, err)
	}
	if len(links) != want {
		t.Fatalf("veths on %s = %d, want %d", bridgeName, len(links), want)
	}
}
