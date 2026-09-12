// Package buildkit provides the BuildKit image adapter.
package buildkit

import (
	"context"
	"errors"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	buildkitclient "github.com/moby/buildkit/client"
)

var errNotImplemented = errors.New("buildkit image adapter: not implemented")

// Adapter is the BuildKit implementation of the image port.
type Adapter struct {
	client *buildkitclient.Client
}

// New constructs a BuildKit image adapter around an existing client.
func New(client *buildkitclient.Client) *Adapter {
	return &Adapter{client: client}
}

// Pull prepares an image reference through BuildKit.
func (a *Adapter) Pull(_ context.Context, _ string) (domain.ImageID, error) {
	return "", errNotImplemented
}

// Remove removes an image reference through BuildKit.
func (a *Adapter) Remove(_ context.Context, _ domain.ImageID) error {
	return errNotImplemented
}

var _ ports.Image = (*Adapter)(nil)
