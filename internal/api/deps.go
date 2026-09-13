package api

import (
	"context"
	"io"
	"time"

	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

const (
	// DefaultStreamLimit bounds concurrent long-lived streams per daemon.
	DefaultStreamLimit = 64
	// DefaultMaxBodyBytes bounds decoded JSON request bodies.
	DefaultMaxBodyBytes int64 = 4 << 20
	// DefaultShutdownTimeout bounds graceful HTTP shutdown.
	DefaultShutdownTimeout = 10 * time.Second
)

// Service is the driving port implemented by internal/app. HTTP handlers depend
// on this interface so handler behavior is testable without any infrastructure.
type Service interface {
	// Info returns daemon-wide counters and backend identity.
	Info(context.Context) (ports.SystemStatus, error)

	// ImageInspect resolves an image reference or ID.
	ImageInspect(context.Context, string) (ports.ImageDetail, error)
	// ImageList returns every stored image.
	ImageList(context.Context) ([]ports.ImageDetail, error)
	// ImagePull pulls an image, streaming Docker JSON progress to out.
	ImagePull(context.Context, ports.PullRequest, io.Writer) error
	// ImageBuild runs a Dockerfile build, streaming Docker JSON progress.
	ImageBuild(context.Context, ports.BuildRequest, io.Writer) error
	// ImageRemove deletes one image reference; force allows removing an image
	// used by a stopped container.
	ImageRemove(context.Context, string, bool) (ports.ImageRemoveResult, error)

	// ContainerCreate creates a container from a Docker create request.
	ContainerCreate(context.Context, ports.ContainerCreateRequest) (ports.ContainerCreateResult, error)
	// ContainerList returns containers, refreshed against runtime state.
	ContainerList(context.Context, bool) ([]domain.Container, error)
	// ContainerInspect resolves a container ID or name.
	ContainerInspect(context.Context, string) (domain.Container, error)
	// ContainerStart starts a created or exited container.
	ContainerStart(context.Context, string) error
	// ContainerStop stops a running container; a nil timeout uses the default.
	ContainerStop(context.Context, string, *time.Duration) error
	// ContainerRemove deletes a container; force stops it first.
	ContainerRemove(context.Context, string, bool) error
	// ContainerLogs streams recorded container logs.
	ContainerLogs(context.Context, string, ports.LogRequest, io.Writer, io.Writer) error

	// ExecCreate registers an exec process and returns its identity.
	ExecCreate(context.Context, string, ports.ExecCreateRequest) (string, error)
	// ExecStart starts a registered exec process and returns its exit code.
	ExecStart(context.Context, string, ports.ExecStartRequest) (int, error)
	// ExecInspect returns the recorded state of an exec process.
	ExecInspect(context.Context, string) (domain.ExecRecord, error)

	// NetworkList returns every configured network.
	NetworkList(context.Context) ([]ports.NetworkDetail, error)
	// NetworkInspect resolves one network by name, full ID, or ID prefix.
	NetworkInspect(context.Context, string) (ports.NetworkDetail, error)
	// NetworkCreate creates a bridge-backed network.
	NetworkCreate(context.Context, ports.NetworkCreateRequest) (ports.NetworkCreateResult, error)
	// NetworkConnect attaches a container to a network.
	NetworkConnect(context.Context, ports.NetworkConnectRequest) error
	// NetworkRemove deletes a network.
	NetworkRemove(context.Context, string) error
}

// Dependencies carries the drivers the API boundary needs. Every field except
// Service is optional.
type Dependencies struct {
	// Service is the application use-case layer. A nil service leaves the
	// Docker data endpoints recognized but unimplemented (501).
	Service Service
	// Logger receives handler-scoped logs; nil uses a no-op logger.
	Logger *zap.Logger
	// StreamLimit bounds concurrent long-lived streams (logs follow, exec
	// start). Zero uses DefaultStreamLimit.
	StreamLimit int
	// MaxBodyBytes bounds decoded JSON request bodies. Zero uses
	// DefaultMaxBodyBytes; negative disables the bound.
	MaxBodyBytes int64
	// ShutdownTimeout bounds http.Server.Shutdown. Zero uses
	// DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}
