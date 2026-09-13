package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// NetworkList implements GET /networks. Docker always exposes the predefined
// bridge, host, and none networks in addition to user networks.
func (s *Service) NetworkList(ctx context.Context) ([]ports.NetworkDetail, error) {
	if _, err := s.networks.EnsureDefaultNetwork(ctx); err != nil {
		return nil, translateError(err)
	}
	details := make([]ports.NetworkDetail, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, ref := range []string{cni.DefaultNetworkName, string(cni.ModeHost), string(cni.ModeNone)} {
		detail, err := s.networks.Resolve(ctx, ref)
		if err != nil {
			return nil, translateError(err)
		}
		details = append(details, detail)
		seen[detail.Name] = struct{}{}
	}
	listed, err := s.networks.List(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	for _, detail := range listed {
		if _, ok := seen[detail.Name]; ok {
			continue
		}
		details = append(details, detail)
	}
	sort.Slice(details, func(i, j int) bool { return details[i].Name < details[j].Name })
	return details, nil
}

// NetworkInspect implements GET /networks/{id}.
func (s *Service) NetworkInspect(ctx context.Context, ref string) (ports.NetworkDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ports.NetworkDetail{}, invalidError("network id or name must not be empty")
	}
	detail, err := s.networks.Resolve(ctx, ref)
	if err != nil && (ref == cni.DefaultNetworkName || strings.EqualFold(ref, "default")) {
		if _, ensureErr := s.networks.EnsureDefaultNetwork(ctx); ensureErr != nil {
			return ports.NetworkDetail{}, translateError(ensureErr)
		}
		detail, err = s.networks.Resolve(ctx, ref)
	}
	if err != nil {
		mapped := translateError(err)
		if errors.Is(mapped, ports.ErrNotFound) {
			return ports.NetworkDetail{}, newDockerError(ports.ErrNotFound, fmt.Sprintf("network %s not found", ref), err)
		}
		return ports.NetworkDetail{}, mapped
	}
	return detail, nil
}

// NetworkCreate implements POST /networks/create.
func (s *Service) NetworkCreate(ctx context.Context, request ports.NetworkCreateRequest) (ports.NetworkCreateResult, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	if strings.TrimSpace(request.Name) == "" {
		return ports.NetworkCreateResult{}, invalidError("network name must not be empty")
	}
	id, err := s.networks.CreateNetwork(ctx, request)
	if err != nil {
		mapped := translateError(err)
		if errors.Is(mapped, ports.ErrConflict) {
			mapped = conflictError("network with name %s already exists", request.Name)
		}
		return ports.NetworkCreateResult{}, mapped
	}
	return ports.NetworkCreateResult{ID: id}, nil
}

// NetworkConnect implements POST /networks/{id}/connect for running
// containers, which are the only ones with a network namespace to join.
func (s *Service) NetworkConnect(ctx context.Context, request ports.NetworkConnectRequest) error {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	container, err := s.resolveContainer(ctx, string(request.Container))
	if err != nil {
		return err
	}
	if state, statusErr := s.runtime.Status(ctx, container.ID); statusErr == nil && state != domain.ContainerStateRunning {
		return conflictError("Container %s is not running", container.ID)
	}
	if s.tasks == nil {
		return serverError("runtime task lookup is not configured")
	}
	pid, err := s.tasks.TaskPID(ctx, container.ID)
	if err != nil {
		return translateError(err)
	}
	result, err := s.networks.Connect(ctx, ports.NetworkConnectRequest{
		Network:   request.Network,
		Container: container.ID,
		NetNS:     fmt.Sprintf("/proc/%d/ns/net", pid),
		Aliases:   request.Aliases,
	})
	if err != nil {
		return translateError(err)
	}
	container.Networks = upsertAttachment(container.Networks, result.Attachment)
	s.save(ctx, container)
	return nil
}

// NetworkRemove implements DELETE /networks/{id}.
func (s *Service) NetworkRemove(ctx context.Context, ref string) error {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	if err := s.networks.Remove(ctx, domain.NetworkID(strings.TrimSpace(ref))); err != nil {
		return translateError(err)
	}
	return nil
}

func upsertAttachment(attachments []domain.NetworkAttachment, attachment domain.NetworkAttachment) []domain.NetworkAttachment {
	for index := range attachments {
		if attachments[index].Name == attachment.Name {
			attachments[index] = attachment
			return attachments
		}
	}
	return append(slices.Clone(attachments), attachment)
}
