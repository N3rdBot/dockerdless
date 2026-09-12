// Package ports defines the inward-facing contracts used by dockerdless.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

// Runtime controls container lifecycle operations.
type Runtime interface {
	Create(context.Context, domain.ContainerSpec) (domain.ContainerID, error)
	Start(context.Context, domain.ContainerID) error
	Stop(context.Context, domain.ContainerID, time.Duration) error
	Remove(context.Context, domain.ContainerID) error
}

// Image provides image lifecycle operations.
type Image interface {
	Pull(context.Context, string) (domain.ImageID, error)
	Remove(context.Context, domain.ImageID) error
}

// Network provides network lifecycle operations.
type Network interface {
	Create(context.Context, string) (domain.NetworkID, error)
	Remove(context.Context, domain.NetworkID) error
}

// IO attaches a stream to a running container.
type IO interface {
	Attach(context.Context, domain.ContainerID) (io.ReadWriteCloser, error)
}

// State persists and retrieves container aggregate state.
type State interface {
	Get(context.Context, domain.ContainerID) (domain.Container, error)
	Save(context.Context, domain.Container) error
	Remove(context.Context, domain.ContainerID) error
}
