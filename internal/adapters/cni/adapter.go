// Package cni provides the CNI network adapter.
package cni

import (
	"context"
	"errors"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"github.com/containernetworking/cni/libcni"
)

var errNotImplemented = errors.New("CNI network adapter: not implemented")

// Adapter is the CNI implementation of the network port.
type Adapter struct {
	config *libcni.CNIConfig
}

// New constructs a CNI network adapter around an existing configuration.
func New(config *libcni.CNIConfig) *Adapter {
	return &Adapter{config: config}
}

// Create prepares a network through CNI.
func (a *Adapter) Create(_ context.Context, _ string) (domain.NetworkID, error) {
	return "", errNotImplemented
}

// Remove removes a network through CNI.
func (a *Adapter) Remove(_ context.Context, _ domain.NetworkID) error {
	return errNotImplemented
}

var _ ports.Network = (*Adapter)(nil)
