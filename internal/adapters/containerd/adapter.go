// Package containerd provides the containerd runtime adapter.
package containerd

import (
	"context"
	"errors"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	containerdclient "github.com/containerd/containerd/v2/client"
)

var errNotImplemented = errors.New("containerd runtime adapter: not implemented")

// Adapter is the containerd implementation of the runtime port.
type Adapter struct {
	client *containerdclient.Client
}

// New constructs a containerd runtime adapter around an existing client.
func New(client *containerdclient.Client) *Adapter {
	return &Adapter{client: client}
}

// Create reserves a container identity in the runtime.
func (a *Adapter) Create(_ context.Context, _ domain.ContainerSpec) (domain.ContainerID, error) {
	return "", errNotImplemented
}

// Start starts a container in the runtime.
func (a *Adapter) Start(_ context.Context, _ domain.ContainerID) error {
	return errNotImplemented
}

// Stop stops a container in the runtime.
func (a *Adapter) Stop(_ context.Context, _ domain.ContainerID, _ time.Duration) error {
	return errNotImplemented
}

// Remove removes a container from the runtime.
func (a *Adapter) Remove(_ context.Context, _ domain.ContainerID) error {
	return errNotImplemented
}

var _ ports.Runtime = (*Adapter)(nil)
