package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"github.com/N3rdBot/dockerdless/internal/streams"
)

var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

const (
	containerImageDigestLabel = "io.dockerdless.image-digest"
	containerCommandLabel     = "io.dockerdless.command"
)

// ContainerCreate implements POST /containers/create: it resolves and pins the
// image digest, ensures the requested network exists, allocates concrete host
// ports before CNI, creates the containerd container, and persists the Docker
// identity.
func (s *Service) ContainerCreate(ctx context.Context, request ports.ContainerCreateRequest) (ports.ContainerCreateResult, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	name := strings.TrimPrefix(strings.TrimSpace(request.Name), "/")
	if name != "" && !containerNamePattern.MatchString(name) {
		return ports.ContainerCreateResult{}, invalidError("Invalid container name (%s), only %s are allowed", name, containerNamePattern)
	}
	if strings.TrimSpace(request.Image) == "" {
		return ports.ContainerCreateResult{}, invalidError("No image specified")
	}
	if name != "" {
		if existing, err := s.registry.GetByName(name); err == nil {
			return ports.ContainerCreateResult{}, conflictError(
				"Conflict. The container name \"/%s\" is already in use by container %q. You have to remove (or rename) that container to be able to reuse that name.",
				name, existing.ID)
		}
	}

	detail, err := s.images.Inspect(ctx, request.Image)
	if err != nil {
		mapped := translateError(err)
		if errors.Is(mapped, ports.ErrNotFound) {
			return ports.ContainerCreateResult{}, newDockerError(ports.ErrNotFound, "No such image: "+request.Image, err)
		}
		return ports.ContainerCreateResult{}, mapped
	}

	mode, err := cni.ParseNetworkMode(request.NetworkMode)
	if err != nil {
		return ports.ContainerCreateResult{}, translateError(err)
	}
	networkName := networkNameFor(request.NetworkMode)
	var network ports.NetworkDetail
	if mode == cni.ModeNetwork {
		if networkName == cni.DefaultNetworkName {
			if _, err := s.networks.EnsureDefaultNetwork(ctx); err != nil {
				return ports.ContainerCreateResult{}, translateError(err)
			}
		}
		network, err = s.networks.Resolve(ctx, networkName)
		if err != nil {
			mapped := translateError(err)
			if errors.Is(mapped, ports.ErrNotFound) {
				return ports.ContainerCreateResult{}, newDockerError(ports.ErrNotFound, fmt.Sprintf("network %s not found", networkName), err)
			}
			return ports.ContainerCreateResult{}, mapped
		}
	}

	bindings := s.cloneBindings(request.PortBindings)
	if len(bindings) > 0 {
		if mode != cni.ModeNetwork {
			return ports.ContainerCreateResult{}, invalidError("conflicting options: port publishing and the container type network mode")
		}
		if s.alloc == nil {
			return ports.ContainerCreateResult{}, serverError("host port allocation is not configured")
		}
		filled, err := s.allocateBindings(bindings)
		if err != nil {
			return ports.ContainerCreateResult{}, translateError(err)
		}
		bindings = filled
	}

	commandJSON, err := json.Marshal(request.Command)
	if err != nil {
		return ports.ContainerCreateResult{}, serverError("encode container command: %v", err)
	}
	labels := cloneLabels(request.Labels)
	if len(request.Mounts) > 0 {
		encodedMounts, err := json.Marshal(request.Mounts)
		if err != nil {
			return ports.ContainerCreateResult{}, serverError("encode container mounts: %v", err)
		}
		labels[ports.LabelMounts] = string(encodedMounts)
	}
	if request.TTY {
		labels[ports.LabelTTY] = "true"
	}
	labels[containerImageDigestLabel] = string(detail.ID)
	labels[containerCommandLabel] = string(commandJSON)

	containerID, err := s.runtime.CreateContainer(ctx, ports.ContainerCreateSpec{
		Name:       name,
		Image:      domain.ImageID(runtimeImageReference(request.Image, detail)),
		Command:    request.Command,
		Entrypoint: request.Entrypoint,
		Env:        request.Env,
		WorkingDir: request.WorkingDir,
		User:       request.User,
		Hostname:   request.Hostname,
		Labels:     labels,
		Mounts:     request.Mounts,
		TTY:        request.TTY,
		OpenStdin:  request.OpenStdin,
	})
	if err != nil {
		s.releaseBindings(bindings)
		if mapped := translateError(err); errors.Is(mapped, ports.ErrConflict) && name != "" {
			return ports.ContainerCreateResult{}, conflictError(
				"Conflict. The container name \"/%s\" is already in use. You have to remove (or rename) that container to be able to reuse that name.", name)
		}
		return ports.ContainerCreateResult{}, translateError(err)
	}

	now := s.now()
	containers := domain.Container{
		ID:             containerID,
		Name:           name,
		Spec:           domain.ContainerSpec{Image: domain.ImageID(request.Image), Command: request.Command, Env: request.Env},
		ImageReference: domain.ImageID(request.Image),
		ImageDigest:    detail.ID,
		Labels:         labels,
		State:          domain.ContainerStateCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
		PortBindings:   bindings,
		Networks:       []domain.NetworkAttachment{networkAttachment(mode, network)},
	}
	if err := s.registry.Save(ctx, containers); err != nil {
		s.releaseBindings(bindings)
		_ = s.runtime.Remove(context.WithoutCancel(ctx), containerID)
		return ports.ContainerCreateResult{}, translateError(err)
	}
	return ports.ContainerCreateResult{ID: containerID}, nil
}

// ContainerList implements GET /containers/json, refreshing runtime state.
func (s *Service) ContainerList(ctx context.Context, all bool) ([]domain.Container, error) {
	containers := s.registry.List()
	listed := make([]domain.Container, 0, len(containers))
	for _, container := range containers {
		updated, changed := s.refresh(ctx, container)
		normalized := normalizeContainerTimestamps(updated, s.now())
		changed = changed || normalized.StartedAt != updated.StartedAt || normalized.FinishedAt != updated.FinishedAt
		updated = normalized
		if changed {
			s.save(ctx, updated)
		}
		if !all && updated.State != domain.ContainerStateRunning {
			continue
		}
		listed = append(listed, updated)
	}
	return listed, nil
}

// ContainerInspect implements GET /containers/{id}/json.
func (s *Service) ContainerInspect(ctx context.Context, ref string) (domain.Container, error) {
	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return domain.Container{}, err
	}
	updated, changed := s.refresh(ctx, container)
	normalized := normalizeContainerTimestamps(updated, s.now())
	changed = changed || normalized.StartedAt != updated.StartedAt || normalized.FinishedAt != updated.FinishedAt
	updated = normalized
	if changed {
		s.save(ctx, updated)
	}
	return updated, nil
}

// ContainerStart implements POST /containers/{id}/start: it opens the log sink,
// starts the containerd task with captured IO, joins the CNI network, and
// persists the running state.
func (s *Service) ContainerStart(ctx context.Context, ref string) error {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return err
	}
	if state, statusErr := s.runtime.Status(ctx, container.ID); statusErr == nil && state == domain.ContainerStateRunning {
		return nil
	}
	sink, err := s.openLogSink(container.ID)
	if err != nil {
		return serverError("failed to open container logs: %v", err)
	}
	if err := s.runtime.StartWithIO(ctx, container.ID, sink.stdoutWriter(), sink.stderrWriter()); err != nil {
		s.closeLogSink(container.ID)
		return translateError(err)
	}
	attachment, connectErr := s.attachNetwork(ctx, container)
	if connectErr != nil {
		state, statusErr := s.runtime.Status(ctx, container.ID)
		if statusErr != nil || (state != domain.ContainerStateExited && state != domain.ContainerStateDead) {
			s.closeLogSink(container.ID)
			_ = s.runtime.Stop(context.WithoutCancel(ctx), container.ID, s.stopTimeout)
			return translateError(connectErr)
		}
		s.logger.Warn("container finished before its network namespace could be joined",
			zap.String("container_id", string(container.ID)),
			zap.Error(connectErr))
		s.releaseBindings(container.PortBindings)
	}
	if state, statusErr := s.runtime.Status(ctx, container.ID); statusErr == nil &&
		state != domain.ContainerStateRunning && state != domain.ContainerStateCreated {
		finished := container
		finished.State = state
		if exit, waitErr := s.runtime.Wait(ctx, container.ID); waitErr == nil {
			finished.ExitCode = int(exit.ExitCode)
			if !exit.ExitedAt.IsZero() {
				finished.FinishedAt = exit.ExitedAt
			}
		}
		s.closeLogSink(container.ID)
		s.save(ctx, finished)
		return nil
	}
	if attachment != nil {
		container.Networks = []domain.NetworkAttachment{attachment.attachment}
		if len(attachment.ports) > 0 {
			container.PortBindings = attachment.ports
		}
	}
	container.State = domain.ContainerStateRunning
	container.StartedAt = s.now()
	container.FinishedAt = time.Time{}
	container.ExitCode = 0
	s.save(ctx, container)
	return nil
}

// ContainerStop implements POST /containers/{id}/stop.
func (s *Service) ContainerStop(ctx context.Context, ref string, timeout *time.Duration) error {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return err
	}
	if container.State == domain.ContainerStateCreated {
		return nil
	}
	effective := s.stopTimeout
	if timeout != nil && *timeout >= 0 {
		effective = *timeout
	}
	if err := s.runtime.Stop(ctx, container.ID, effective); err != nil {
		return translateError(err)
	}
	updated, refreshed := s.refresh(ctx, container)
	if !refreshed {
		updated = container
	}
	updated.State = domain.ContainerStateExited
	s.closeLogSink(container.ID)
	s.save(ctx, updated)
	return nil
}

// ContainerRemove implements DELETE /containers/{id}.
func (s *Service) ContainerRemove(ctx context.Context, ref string, force bool) error {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return err
	}
	state, statusErr := s.runtime.Status(ctx, container.ID)
	if statusErr != nil {
		return translateError(statusErr)
	}
	if force && state != domain.ContainerStateExited {
		_ = s.runtime.Stop(context.WithoutCancel(ctx), container.ID, 0)
		state = domain.ContainerStateExited
	}
	switch state {
	case domain.ContainerStateRunning, domain.ContainerStatePaused, domain.ContainerStateRestarting:
		return conflictError("You cannot remove a running container %s. Stop the container before attempting removal or force remove", container.ID)
	}
	for _, disconnectErr := range s.networks.DisconnectAll(ctx, container.ID) {
		s.logger.Warn("failed to detach container network",
			zap.String("container_id", string(container.ID)),
			zap.Error(disconnectErr))
	}
	if container.State == domain.ContainerStateCreated {
		s.releaseBindings(container.PortBindings)
	}
	if err := s.runtime.Remove(ctx, container.ID); err != nil {
		return translateError(err)
	}
	if err := s.registry.Remove(ctx, container.ID); err != nil && !errors.Is(err, domain.ErrContainerNotFound) {
		return translateError(err)
	}
	s.discardLogs(container.ID)
	return nil
}

// ContainerLogs implements GET /containers/{id}/logs using the shared stream
// reader so follow, tail, since, and timestamps behave like Docker's.
func (s *Service) ContainerLogs(ctx context.Context, ref string, options ports.LogRequest, stdout, stderr io.Writer) error {
	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return err
	}
	path := s.logPath(container.ID)
	if _, statErr := os.Stat(path); statErr != nil && !options.Follow {
		return nil
	}
	out := stdout
	if !options.Stdout {
		out = io.Discard
	}
	errOut := stderr
	if !options.Stderr {
		errOut = io.Discard
	}
	followCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if options.Follow {
		go s.cancelFollowWhenExited(followCtx, container.ID, cancel)
	}
	return streams.ReadLogs(followCtx, path, streams.LogOptions{
		Follow:     options.Follow,
		Tail:       options.Tail,
		Since:      options.Since,
		Until:      options.Until,
		Timestamps: options.Timestamps,
		Format:     streams.LogFormatCRI,
	}, out, errOut)
}

func (s *Service) cancelFollowWhenExited(ctx context.Context, id domain.ContainerID, cancel context.CancelFunc) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		state, err := s.runtime.Status(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("failed to observe container while following logs",
					zap.String("container_id", string(id)), zap.Error(err))
				cancel()
			}
			return
		}
		if state != domain.ContainerStateRunning && state != domain.ContainerStateCreated {
			cancel()
			return
		}
	}
}

type containerAttachment struct {
	attachment domain.NetworkAttachment
	ports      []domain.PortBinding
}

// attachNetwork joins the container to its network. Host and none modes skip
// CNI entirely. Named networks release the create-time port reservations and
// let the CNI adapter re-reserve the same concrete ports before portmap runs.
func (s *Service) attachNetwork(ctx context.Context, container domain.Container) (*containerAttachment, error) {
	if len(container.Networks) == 0 {
		return nil, nil
	}
	network := container.Networks[0]
	if network.Name == "" || network.Name == string(cni.ModeHost) || network.Name == string(cni.ModeNone) {
		return nil, nil
	}
	if s.tasks == nil {
		return nil, serverError("runtime task lookup is not configured")
	}
	pid, err := s.tasks.TaskPID(ctx, container.ID)
	if err != nil {
		return nil, translateError(err)
	}
	request := ports.NetworkConnectRequest{
		Network:   network.Name,
		Container: container.ID,
		NetNS:     fmt.Sprintf("/proc/%d/ns/net", pid),
		Ports:     container.PortBindings,
	}
	reservedConnector, hasReservedConnector := s.networks.(interface {
		ConnectReserved(context.Context, ports.NetworkConnectRequest) (ports.NetworkAttachmentResult, error)
	})
	var result ports.NetworkAttachmentResult
	if hasReservedConnector {
		result, err = reservedConnector.ConnectReserved(ctx, request)
	} else {
		s.allocMu.Lock()
		if len(container.PortBindings) > 0 && s.alloc != nil {
			s.alloc.Release(s.reservationsFor(container.PortBindings)...)
		}
		s.allocMu.Unlock()
		result, err = s.networks.Connect(ctx, request)
	}
	if err != nil {
		if hasReservedConnector && len(container.PortBindings) > 0 && s.alloc != nil {
			s.allocMu.Lock()
			_, _, reserveErr := s.alloc.AllocateBindings(container.PortBindings)
			s.allocMu.Unlock()
			if reserveErr != nil && !errors.Is(reserveErr, cni.ErrPortInUse) {
				s.logger.Warn("failed to restore container port reservations after network failure",
					zap.String("container_id", string(container.ID)), zap.Error(reserveErr))
			}
		}
		return nil, err
	}
	return &containerAttachment{attachment: result.Attachment, ports: result.Ports}, nil
}

// runtimeImageReference returns the containerd-canonical reference for a
// resolved image. The runtime adapter resolves images by exact stored name,
// while the API accepts familiar references and ID prefixes.
func runtimeImageReference(requested string, detail ports.ImageDetail) string {
	for _, candidates := range [][]string{detail.RepoTags, detail.RepoDigests} {
		for _, candidate := range candidates {
			if normalized, err := buildkit.NormalizeReference(candidate); err == nil {
				return normalized
			}
		}
	}
	if normalized, err := buildkit.NormalizeReference(requested); err == nil {
		return normalized
	}
	return requested
}

func (s *Service) allocateBindings(bindings []domain.PortBinding) ([]domain.PortBinding, error) {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	filled, _, err := s.alloc.AllocateBindings(bindings)
	return filled, err
}

func networkNameFor(mode string) string {
	trimmed := strings.TrimSpace(mode)
	switch strings.ToLower(trimmed) {
	case "", "default", cni.DefaultNetworkName:
		return cni.DefaultNetworkName
	default:
		return trimmed
	}
}

func networkAttachment(mode cni.Mode, network ports.NetworkDetail) domain.NetworkAttachment {
	if mode != cni.ModeNetwork {
		return domain.NetworkAttachment{Name: string(mode)}
	}
	return domain.NetworkAttachment{NetworkID: network.ID, Name: network.Name}
}

// refresh merges runtime state into a container without erasing Docker's
// created state for containers whose task never started.
func (s *Service) refresh(ctx context.Context, container domain.Container) (domain.Container, bool) {
	state, err := s.runtime.Status(ctx, container.ID)
	if err != nil {
		s.logger.Warn("failed to refresh container state",
			zap.String("container_id", string(container.ID)), zap.Error(err))
		return container, false
	}
	if container.State == domain.ContainerStateCreated && state != domain.ContainerStateRunning {
		return container, false
	}
	if container.State == state && state == domain.ContainerStateRunning {
		return container, false
	}
	updated := container
	updated.State = state
	if state != domain.ContainerStateRunning && state != domain.ContainerStateCreated {
		s.closeLogSink(container.ID)
		waitCtx, cancel := context.WithTimeout(ctx, exitStatusTimeout)
		exit, waitErr := s.runtime.Wait(waitCtx, container.ID)
		cancel()
		if waitErr == nil {
			updated.ExitCode = int(exit.ExitCode)
			if !exit.ExitedAt.IsZero() {
				updated.FinishedAt = exit.ExitedAt
				updated.UpdatedAt = exit.ExitedAt
			}
		}
	}
	changed := updated.State != container.State ||
		updated.ExitCode != container.ExitCode ||
		!updated.FinishedAt.Equal(container.FinishedAt)
	return updated, changed
}

func normalizeContainerTimestamps(container domain.Container, now time.Time) domain.Container {
	if container.State == domain.ContainerStateRunning || container.State == domain.ContainerStatePaused {
		if container.StartedAt.IsZero() {
			container.StartedAt = container.CreatedAt
			if container.StartedAt.IsZero() {
				container.StartedAt = now
			}
		}
	}
	if container.State == domain.ContainerStateExited && container.FinishedAt.IsZero() {
		container.FinishedAt = container.CreatedAt
		if container.FinishedAt.IsZero() {
			container.FinishedAt = now
		}
	}
	return container
}
