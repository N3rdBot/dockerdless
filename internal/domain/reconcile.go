package domain

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrDuplicateContainerMetadata = errors.New("duplicate container metadata")
	ErrDuplicateTaskSnapshot      = errors.New("duplicate task snapshot")
	ErrInvalidTaskSnapshot        = errors.New("invalid task snapshot")
)

type TaskState string

const (
	TaskStateCreated    TaskState = "created"
	TaskStateRunning    TaskState = "running"
	TaskStatePaused     TaskState = "paused"
	TaskStateRestarting TaskState = "restarting"
	TaskStateStopped    TaskState = "stopped"
	TaskStateExited     TaskState = "exited"
	TaskStateDead       TaskState = "dead"

	TaskCreated    = TaskStateCreated
	TaskRunning    = TaskStateRunning
	TaskPaused     = TaskStatePaused
	TaskRestarting = TaskStateRestarting
	TaskStopped    = TaskStateStopped
	TaskExited     = TaskStateExited
	TaskDead       = TaskStateDead
)

type TaskStatus = TaskState

const (
	TaskStatusCreated    = TaskStateCreated
	TaskStatusRunning    = TaskStateRunning
	TaskStatusPaused     = TaskStatePaused
	TaskStatusRestarting = TaskStateRestarting
	TaskStatusStopped    = TaskStateStopped
	TaskStatusExited     = TaskStateExited
	TaskStatusDead       = TaskStateDead
)

type TaskSnapshot struct {
	ID          ContainerID `json:"Id,omitempty"`
	ContainerID ContainerID `json:"ContainerID,omitempty"`
	Status      TaskState   `json:"Status"`
	ExitCode    int         `json:"ExitCode"`
	OOMKilled   bool        `json:"OOMKilled"`
	Error       string      `json:"Error,omitempty"`
	StartedAt   time.Time   `json:"StartedAt,omitzero"`
	FinishedAt  time.Time   `json:"FinishedAt,omitzero"`
}

type ReconcileResult struct {
	Containers []Container   `json:"Containers"`
	Stale      []ContainerID `json:"Stale"`
	Cleanup    []ContainerID `json:"Cleanup"`
}

type ReconciliationResult = ReconcileResult

func Reconcile(metadata []Container, tasks []TaskSnapshot) (ReconcileResult, error) {
	metadataByID := make(map[ContainerID]Container, len(metadata))
	for _, container := range metadata {
		if container.ID == "" {
			return ReconcileResult{}, fmt.Errorf("metadata: %w", ErrEmptyIdentity)
		}
		if _, exists := metadataByID[container.ID]; exists {
			return ReconcileResult{}, fmt.Errorf("metadata %s: %w", container.ID, ErrDuplicateContainerMetadata)
		}
		metadataByID[container.ID] = container
	}

	tasksByID := make(map[ContainerID]TaskSnapshot, len(tasks))
	for _, task := range tasks {
		id, err := taskIdentity(task)
		if err != nil {
			return ReconcileResult{}, err
		}
		if _, exists := tasksByID[id]; exists {
			return ReconcileResult{}, fmt.Errorf("task %s: %w", id, ErrDuplicateTaskSnapshot)
		}
		task.ContainerID = id
		tasksByID[id] = task
	}

	result := ReconcileResult{
		Containers: make([]Container, 0, len(metadata)),
		Stale:      make([]ContainerID, 0),
		Cleanup:    make([]ContainerID, 0),
	}
	for _, container := range metadata {
		task, ok := tasksByID[container.ID]
		if !ok {
			container.State = ContainerStateExited
			container.Dead = false
			result.Stale = append(result.Stale, container.ID)
		} else {
			if err := applyTask(&container, task); err != nil {
				return ReconcileResult{}, fmt.Errorf("container %s: %w", container.ID, err)
			}
			delete(tasksByID, container.ID)
		}
		if container.ImageReference == "" {
			container.ImageReference = container.Spec.Image
		}
		if container.Spec.Image == "" {
			container.Spec.Image = container.ImageReference
		}
		result.Containers = append(result.Containers, container)
	}
	for id := range tasksByID {
		result.Cleanup = append(result.Cleanup, id)
	}
	slices.Sort(result.Cleanup)
	return result, nil
}

func taskIdentity(task TaskSnapshot) (ContainerID, error) {
	switch {
	case task.ContainerID != "" && task.ID != "" && task.ContainerID != task.ID:
		return "", fmt.Errorf("%w: task IDs disagree", ErrInvalidTaskSnapshot)
	case task.ContainerID != "":
		return task.ContainerID, nil
	case task.ID != "":
		return task.ID, nil
	default:
		return "", fmt.Errorf("%w: task identity is empty", ErrInvalidTaskSnapshot)
	}
}

func applyTask(container *Container, task TaskSnapshot) error {
	container.ExitCode = task.ExitCode
	container.OOMKilled = task.OOMKilled
	container.Error = task.Error
	container.StartedAt = task.StartedAt
	container.FinishedAt = task.FinishedAt
	if !task.FinishedAt.IsZero() {
		container.UpdatedAt = task.FinishedAt
	}

	switch task.Status {
	case TaskStateCreated:
		container.State, container.Dead = ContainerStateCreated, false
	case TaskStateRunning:
		container.State, container.Dead = ContainerStateRunning, false
	case TaskStatePaused:
		container.State, container.Dead = ContainerStatePaused, false
	case TaskStateRestarting:
		container.State, container.Dead = ContainerStateRestarting, false
	case TaskStateStopped, TaskStateExited:
		container.State, container.Dead = ContainerStateExited, false
	case TaskStateDead:
		container.State, container.Dead = ContainerStateDead, true
	default:
		return fmt.Errorf("%w: unsupported task status %q", ErrInvalidTaskSnapshot, task.Status)
	}
	return nil
}
