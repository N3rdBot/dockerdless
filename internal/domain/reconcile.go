package domain

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	// ErrDuplicateContainerMetadata means two metadata records share an ID.
	ErrDuplicateContainerMetadata = errors.New("duplicate container metadata")
	// ErrDuplicateTaskSnapshot means two runtime snapshots share a container ID.
	ErrDuplicateTaskSnapshot = errors.New("duplicate task snapshot")
	// ErrInvalidTaskSnapshot means a task snapshot has no usable identity or state.
	ErrInvalidTaskSnapshot = errors.New("invalid task snapshot")
)

// TaskState is the state reported by the containerd Tasks service.
type TaskState string

const (
	// TaskStateCreated means a task object exists but is not running.
	TaskStateCreated TaskState = "created"
	// TaskStateRunning means the task process is running.
	TaskStateRunning TaskState = "running"
	// TaskStatePaused means the task process is paused.
	TaskStatePaused TaskState = "paused"
	// TaskStateRestarting means the task is in a restart transition.
	TaskStateRestarting TaskState = "restarting"
	// TaskStateStopped means the task process has exited.
	TaskStateStopped TaskState = "stopped"
	// TaskStateExited is accepted for snapshots already normalized to Docker terminology.
	TaskStateExited TaskState = "exited"
	// TaskStateDead means the task's runtime object is unusable.
	TaskStateDead TaskState = "dead"

	TaskCreated    = TaskStateCreated
	TaskRunning    = TaskStateRunning
	TaskPaused     = TaskStatePaused
	TaskRestarting = TaskStateRestarting
	TaskStopped    = TaskStateStopped
	TaskExited     = TaskStateExited
	TaskDead       = TaskStateDead
)

// TaskStatus is a compatibility name for TaskState.
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

// TaskSnapshot is the runtime half of containerd's split container model.
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

// ReconcileResult contains runtime-derived containers and cleanup classes.
type ReconcileResult struct {
	Containers []Container   `json:"Containers"`
	Stale      []ContainerID `json:"Stale"`
	Cleanup    []ContainerID `json:"Cleanup"`
}

// ReconciliationResult is a compatibility name for ReconcileResult.
type ReconciliationResult = ReconcileResult

// Reconcile joins metadata records to runtime task snapshots.
// Metadata without a task is exited and stale; it is never running.
// A task without metadata is marked for cleanup because its runtime object
// cannot be safely addressed through the Docker identity index.
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
		container.State = ContainerStateCreated
		container.Dead = false
	case TaskStateRunning:
		container.State = ContainerStateRunning
		container.Dead = false
	case TaskStatePaused:
		container.State = ContainerStatePaused
		container.Dead = false
	case TaskStateRestarting:
		container.State = ContainerStateRestarting
		container.Dead = false
	case TaskStateStopped, TaskStateExited:
		container.State = ContainerStateExited
		container.Dead = false
	case TaskStateDead:
		container.State = ContainerStateDead
		container.Dead = true
	default:
		return fmt.Errorf("%w: unsupported task status %q", ErrInvalidTaskSnapshot, task.Status)
	}
	return nil
}
