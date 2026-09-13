package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/observability"
	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"go.uber.org/zap"
)

type snapshotLoader func(context.Context) ([]domain.Container, []domain.TaskSnapshot, error)

const (
	containerImageDigestLabel = "io.dockerdless.image-digest"
	containerCommandLabel     = "io.dockerdless.command"
)

func reconcileAtStartup(ctx context.Context, registry *domain.Registry, logger *observability.Logger, load snapshotLoader) error {
	metadata, tasks, err := load(ctx)
	if err != nil {
		return err
	}
	result, err := domain.Reconcile(metadata, tasks)
	if err != nil {
		return err
	}
	for _, container := range result.Containers {
		if err := registry.Save(ctx, container); err != nil {
			return fmt.Errorf("save recovered container %s: %w", container.ID, err)
		}
		topic := ""
		switch container.State {
		case domain.ContainerStateRunning:
			topic = domain.TaskStartEventTopic
		case domain.ContainerStateExited, domain.ContainerStateDead:
			topic = domain.TaskExitEventTopic
		}
		if action, ok := domain.LookupDockerAction(topic); ok {
			logger.Info("recovered container state", zap.String("container_id", string(container.ID)), zap.String("status", string(container.State)), zap.String("event_topic", topic), zap.String("docker_action", string(action)))
		}
	}
	logger.Info("container state reconciliation complete", zap.Int("reconciled_count", len(result.Containers)), zap.Int("stale_count", len(result.Stale)), zap.Int("cleanup_count", len(result.Cleanup)))
	return nil
}

func loadContainerdSnapshot(ctx context.Context, client *containerdclient.Client, namespace string) ([]domain.Container, []domain.TaskSnapshot, error) {
	ctx = namespaces.WithNamespace(ctx, namespace)
	containers, err := client.Containers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list container metadata: %w", err)
	}
	metadata := make([]domain.Container, 0, len(containers))
	tasks := make([]domain.TaskSnapshot, 0, len(containers))
	for _, container := range containers {
		info, err := container.Info(ctx, containerdclient.WithoutRefreshedMetadata)
		if err != nil {
			return nil, nil, fmt.Errorf("read container %s: %w", container.ID(), err)
		}
		runtimeSpec, err := container.Spec(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read container spec %s: %w", info.ID, err)
		}
		labels := make(map[string]string, len(info.Labels)+len(runtimeSpec.Annotations))
		maps.Copy(labels, info.Labels)
		maps.Copy(labels, runtimeSpec.Annotations)
		metadataContainer, err := restoredContainer(info.ID, info.Image, labels, info.CreatedAt, info.UpdatedAt)
		if err != nil {
			return nil, nil, err
		}
		metadata = append(metadata, metadataContainer)

		task, err := container.Task(ctx, nil)
		if err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			return nil, nil, fmt.Errorf("read task %s: %w", info.ID, err)
		}
		status, err := task.Status(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read task status %s: %w", info.ID, err)
		}
		tasks = append(tasks, domain.TaskSnapshot{ContainerID: domain.ContainerID(info.ID), Status: domain.TaskState(status.Status), ExitCode: int(status.ExitStatus), FinishedAt: status.ExitTime})
	}
	return metadata, tasks, nil
}

func restoredContainer(id, image string, labels map[string]string, createdAt, updatedAt time.Time) (domain.Container, error) {
	imageID := domain.ImageID(image)
	imageDigest := domain.ImageID(labels[containerImageDigestLabel])
	if imageDigest == "" {
		imageDigest = digestFromReference(imageID)
	}
	var command []string
	if raw := labels[containerCommandLabel]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			return domain.Container{}, fmt.Errorf("decode command label for container %s: %w", id, err)
		}
	}
	return domain.Container{
		ID:             domain.ContainerID(id),
		Name:           labels["io.dockerdless.name"],
		Spec:           domain.ContainerSpec{Image: imageID, Command: command},
		ImageReference: imageID,
		ImageDigest:    imageDigest,
		Labels:         labels,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}

func digestFromReference(image domain.ImageID) domain.ImageID {
	value := string(image)
	if index := strings.LastIndex(value, "@"); index >= 0 && index+1 < len(value) {
		return domain.ImageID(value[index+1:])
	}
	return ""
}
