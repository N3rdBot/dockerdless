// Package app orchestrates the dockerdless use cases: it composes the domain
// registry, the containerd/BuildKit/CNI adapters, and the stream helpers behind
// the ports contracts so HTTP handlers stay thin.
package app

import (
	"context"
	"io"
	"slices"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/adapters/containerd"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// NewRuntimeAdapter normalizes the containerd adapter onto the runtime port.
func NewRuntimeAdapter(adapter *containerd.Adapter) ports.RuntimeController {
	if adapter == nil {
		return nil
	}
	return &runtimeAdapter{Adapter: adapter}
}

type runtimeAdapter struct {
	*containerd.Adapter
}

func (a *runtimeAdapter) CreateContainer(ctx context.Context, spec ports.ContainerCreateSpec) (domain.ContainerID, error) {
	mounts := make([]containerd.Mount, 0, len(spec.Mounts))
	for _, mount := range spec.Mounts {
		mounts = append(mounts, containerd.Mount{
			Type:        mount.Type,
			Source:      mount.Source,
			Destination: mount.Destination,
			ReadOnly:    mount.ReadOnly,
			Options:     mount.Options,
		})
	}
	return a.Adapter.CreateContainer(ctx, containerd.Config{
		ID:          spec.ID,
		Name:        spec.Name,
		Image:       spec.Image,
		Command:     spec.Command,
		Entrypoint:  spec.Entrypoint,
		Env:         spec.Env,
		WorkingDir:  spec.WorkingDir,
		User:        spec.User,
		Hostname:    spec.Hostname,
		Labels:      spec.Labels,
		Mounts:      mounts,
		Terminal:    spec.TTY,
		OpenStdin:   spec.OpenStdin,
		Snapshotter: spec.Snapshotter,
	})
}

func (a *runtimeAdapter) Wait(ctx context.Context, id domain.ContainerID) (ports.ProcessExit, error) {
	result, err := a.Adapter.Wait(ctx, id)
	if err != nil {
		return ports.ProcessExit{}, err
	}
	return ports.ProcessExit{ExitCode: result.ExitCode, ExitedAt: result.ExitedAt}, nil
}

func (a *runtimeAdapter) StartWithIO(ctx context.Context, id domain.ContainerID, stdout, stderr io.Writer) error {
	return a.StartWithOptions(ctx, id, containerd.StartOptions{Stdout: stdout, Stderr: stderr})
}

func (a *runtimeAdapter) Exec(ctx context.Context, id domain.ContainerID, request ports.ExecRequest) (ports.ExecResult, error) {
	result, err := a.Adapter.Exec(ctx, id, containerd.ExecConfig{
		ID:         request.ID,
		Command:    request.Command,
		Env:        request.Env,
		WorkingDir: request.WorkingDir,
		User:       request.User,
		Terminal:   request.TTY,
		Stdin:      request.Stdin,
		Stdout:     request.Stdout,
		Stderr:     request.Stderr,
		Detach:     request.Detach,
		Width:      request.Width,
		Height:     request.Height,
	})
	if err != nil {
		return ports.ExecResult{}, err
	}
	return ports.ExecResult{
		ID:         result.ID,
		ExitCode:   result.ExitCode,
		Running:    result.Detached || result.ExitCode == nil,
		Pid:        int(result.Pid),
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
	}, nil
}

// NewImageAdapter normalizes the BuildKit adapter onto the image port.
func NewImageAdapter(adapter *buildkit.Adapter) ports.ImageController {
	if adapter == nil {
		return nil
	}
	return &imageAdapter{Adapter: adapter}
}

type imageAdapter struct {
	*buildkit.Adapter
}

func (a *imageAdapter) PullImage(ctx context.Context, ref string, request ports.PullRequest) (domain.ImageID, error) {
	return a.Adapter.PullImage(ctx, ref, buildkit.PullOptions{
		Platform: request.Platform,
		Auth:     registryAuth(request.Auth),
	})
}

func (a *imageAdapter) Inspect(ctx context.Context, ref string) (ports.ImageDetail, error) {
	detail, err := a.Adapter.Inspect(ctx, ref)
	if err != nil {
		return ports.ImageDetail{}, err
	}
	return imageDetail(detail), nil
}

func (a *imageAdapter) List(ctx context.Context) ([]ports.ImageDetail, error) {
	details, err := a.Adapter.List(ctx)
	if err != nil {
		return nil, err
	}
	converted := make([]ports.ImageDetail, 0, len(details))
	for _, detail := range details {
		converted = append(converted, imageDetail(detail))
	}
	return converted, nil
}

func (a *imageAdapter) Build(ctx context.Context, request ports.BuildRequest, out io.Writer) error {
	return a.Adapter.Build(ctx, buildkit.BuildRequest{
		Context:    request.Context,
		Tag:        request.Tag,
		Dockerfile: request.Dockerfile,
		Platform:   request.Platform,
		BuildArgs:  request.BuildArgs,
		Target:     request.Target,
		NoCache:    request.NoCache,
		Auth:       registryAuth(request.Auth),
	}, out)
}

// NewNetworkAdapter normalizes the CNI adapter onto the network port.
func NewNetworkAdapter(adapter *cni.Adapter) ports.NetworkController {
	if adapter == nil {
		return nil
	}
	return &networkAdapter{Adapter: adapter}
}

type networkAdapter struct {
	*cni.Adapter
}

func (a *networkAdapter) Resolve(ctx context.Context, ref string) (ports.NetworkDetail, error) {
	network, err := a.Adapter.Resolve(ctx, ref)
	if err != nil {
		return ports.NetworkDetail{}, err
	}
	return networkDetail(network), nil
}

func (a *networkAdapter) List(ctx context.Context) ([]ports.NetworkDetail, error) {
	networks, err := a.Adapter.List(ctx)
	if err != nil {
		return nil, err
	}
	details := make([]ports.NetworkDetail, 0, len(networks))
	for _, network := range networks {
		details = append(details, networkDetail(network))
	}
	return details, nil
}

func (a *networkAdapter) CreateNetwork(ctx context.Context, request ports.NetworkCreateRequest) (domain.NetworkID, error) {
	return a.Adapter.CreateNetwork(ctx, cni.CreateNetworkRequest{
		Name:       request.Name,
		Driver:     request.Driver,
		EnableIPv6: request.EnableIPv6,
		Labels:     request.Labels,
	})
}

func (a *networkAdapter) Connect(ctx context.Context, request ports.NetworkConnectRequest) (ports.NetworkAttachmentResult, error) {
	result, err := a.Adapter.Connect(ctx, cni.ConnectRequest{
		Network:   request.Network,
		Container: request.Container,
		NetNS:     request.NetNS,
		IfName:    request.IfName,
		Ports:     request.Ports,
		Aliases:   request.Aliases,
	})
	if err != nil {
		return ports.NetworkAttachmentResult{}, err
	}
	return ports.NetworkAttachmentResult{Attachment: result.Attachment, Ports: result.Ports}, nil
}

// NewPortAllocator normalizes the CNI port allocator onto the ports contract.
func NewPortAllocator(allocator *cni.PortAllocator) ports.PortAllocator {
	if allocator == nil {
		return nil
	}
	return &portAllocator{allocator: allocator}
}

type portAllocator struct {
	allocator *cni.PortAllocator
}

func (a *portAllocator) AllocateBindings(bindings []domain.PortBinding) ([]domain.PortBinding, []ports.PortReservation, error) {
	filled, allocs, err := a.allocator.AllocateBindings(bindings)
	if err != nil {
		return nil, nil, err
	}
	reservations := make([]ports.PortReservation, 0, len(allocs))
	for _, alloc := range allocs {
		reservations = append(reservations, ports.PortReservation{
			Protocol: alloc.Protocol,
			HostIP:   alloc.HostIP,
			HostPort: alloc.HostPort,
		})
	}
	return filled, reservations, nil
}

func (a *portAllocator) Release(reservations ...ports.PortReservation) {
	allocs := make([]cni.PortAllocation, 0, len(reservations))
	for _, reservation := range reservations {
		allocs = append(allocs, cni.PortAllocation{
			Protocol: reservation.Protocol,
			HostIP:   reservation.HostIP,
			HostPort: reservation.HostPort,
		})
	}
	a.allocator.Release(allocs...)
}

func registryAuth(auth *ports.RegistryAuth) *buildkit.RegistryAuth {
	if auth == nil {
		return nil
	}
	return &buildkit.RegistryAuth{
		Username:      auth.Username,
		Password:      auth.Password,
		IdentityToken: auth.IdentityToken,
		RegistryToken: auth.RegistryToken,
		ServerAddress: auth.ServerAddress,
	}
}

func imageDetail(detail buildkit.ImageDetail) ports.ImageDetail {
	return ports.ImageDetail{
		ID:          detail.ID,
		RepoTags:    slices.Clone(detail.RepoTags),
		RepoDigests: slices.Clone(detail.RepoDigests),
		Platform:    detail.Platform,
		Size:        detail.Size,
		Created:     detail.Created,
		Labels:      detail.Labels,
	}
}

func networkDetail(network cni.Network) ports.NetworkDetail {
	return ports.NetworkDetail{
		ID:      network.ID,
		Name:    network.Name,
		Driver:  network.Driver,
		Mode:    string(network.Mode),
		Subnet:  network.Subnet,
		Gateway: network.Gateway,
		Labels:  network.Labels,
	}
}
