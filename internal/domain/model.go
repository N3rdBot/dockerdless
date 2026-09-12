// Package domain contains Docker identity, state, and event models.
package domain

import (
	"errors"
	"strings"
	"time"
)

// ErrEmptyIdentity is returned when a Docker resource identity is blank.
var ErrEmptyIdentity = errors.New("resource identity must not be empty")

// ContainerID identifies a container across the domain boundary.
type ContainerID string

// ImageID identifies an image reference across the domain boundary.
type ImageID string

// NetworkID identifies a network across the domain boundary.
type NetworkID string

// ContainerState is the lifecycle state of a container.
type ContainerState string

const (
	// ContainerStateUnknown means the runtime has not reported a state.
	ContainerStateUnknown ContainerState = "unknown"
	// ContainerStateCreated means the container exists but has not started.
	ContainerStateCreated ContainerState = "created"
	// ContainerStateRunning means the container process is running.
	ContainerStateRunning ContainerState = "running"
	// ContainerStateStopped means the container process has exited.
	ContainerStateStopped ContainerState = "stopped"
)

// ContainerSpec describes the desired container configuration.
type ContainerSpec struct {
	Image   ImageID
	Command []string
	Env     map[string]string
}

// Container is the aggregate root for container lifecycle state.
type Container struct {
	ID        ContainerID
	Spec      ContainerSpec
	State     ContainerState
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventType identifies a domain event emitted by a lifecycle transition.
type EventType string

const (
	// EventContainerCreated records creation of a container.
	EventContainerCreated EventType = "container.created"
	// EventContainerStarted records the start of a container.
	EventContainerStarted EventType = "container.started"
	// EventContainerStopped records the stop of a container.
	EventContainerStopped EventType = "container.stopped"
)

// Event records a state transition without coupling the domain to a broker.
type Event struct {
	Type        EventType
	ContainerID ContainerID
	OccurredAt  time.Time
	Metadata    map[string]string
}

// NewContainerID validates and constructs a container identity.
func NewContainerID(value string) (ContainerID, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrEmptyIdentity
	}
	return ContainerID(value), nil
}

// NewImageID validates and constructs an image identity.
func NewImageID(value string) (ImageID, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrEmptyIdentity
	}
	return ImageID(value), nil
}

// NewNetworkID validates and constructs a network identity.
func NewNetworkID(value string) (NetworkID, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrEmptyIdentity
	}
	return NetworkID(value), nil
}
