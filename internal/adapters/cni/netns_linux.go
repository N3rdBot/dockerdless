//go:build linux

package cni

import (
	"fmt"
	"net"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// SetnsProbe inspects a container interface by temporarily entering its
// network namespace on a locked OS thread.
type SetnsProbe struct{}

var _ NetNSProbe = SetnsProbe{}

// InterfaceInfo returns the addresses and MAC of ifName inside netnsPath.
func (SetnsProbe) InterfaceInfo(netnsPath, ifName string) (InterfaceInfo, error) {
	target, err := os.Open(netnsPath)
	if err != nil {
		return InterfaceInfo{}, fmt.Errorf("cni: opening netns %q: %w", netnsPath, err)
	}
	defer func() { _ = target.Close() }()
	origin, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return InterfaceInfo{}, fmt.Errorf("cni: opening current netns: %w", err)
	}
	defer func() { _ = origin.Close() }()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		return InterfaceInfo{}, fmt.Errorf("cni: entering netns %q: %w", netnsPath, err)
	}
	defer func() { _ = unix.Setns(int(origin.Fd()), unix.CLONE_NEWNET) }()

	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return InterfaceInfo{}, fmt.Errorf("cni: interface %q in netns %q: %w", ifName, netnsPath, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return InterfaceInfo{}, fmt.Errorf("cni: addresses for %q: %w", ifName, err)
	}
	info := InterfaceInfo{MAC: iface.HardwareAddr.String()}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			info.IPAddresses = append(info.IPAddresses, *ipNet)
		}
	}
	return info, nil
}
