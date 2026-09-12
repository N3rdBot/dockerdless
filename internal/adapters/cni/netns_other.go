//go:build !linux

package cni

import "errors"

// ErrUnsupportedPlatform reports live netns probing outside Linux.
var ErrUnsupportedPlatform = errors.New("cni: live netns probing requires linux")

// SetnsProbe is unavailable outside Linux.
type SetnsProbe struct{}

var _ NetNSProbe = SetnsProbe{}

// InterfaceInfo always fails on non-Linux platforms.
func (SetnsProbe) InterfaceInfo(string, string) (InterfaceInfo, error) {
	return InterfaceInfo{}, ErrUnsupportedPlatform
}
