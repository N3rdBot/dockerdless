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

// ContainerState is the Docker-compatible lifecycle state of a container.
type ContainerState string

const (
	// ContainerStateUnknown means the runtime has not reported a state.
	ContainerStateUnknown ContainerState = "unknown"
	// ContainerStateCreated means the container exists but has not started.
	ContainerStateCreated ContainerState = "created"
	// ContainerStateRunning means the container process is running.
	ContainerStateRunning ContainerState = "running"
	// ContainerStatePaused means the container process is paused.
	ContainerStatePaused ContainerState = "paused"
	// ContainerStateRestarting means the container is being restarted.
	ContainerStateRestarting ContainerState = "restarting"
	// ContainerStateRemoving means the container is being removed.
	ContainerStateRemoving ContainerState = "removing"
	// ContainerStateExited means the container process has exited.
	ContainerStateExited ContainerState = "exited"
	// ContainerStateDead means the container could not be removed cleanly.
	ContainerStateDead ContainerState = "dead"
	// ContainerStateStopped preserves the original domain name as an alias.
	ContainerStateStopped ContainerState = ContainerStateExited
)

// IsDockerState reports whether the state is one of Docker's inspect states.
func (s ContainerState) IsDockerState() bool {
	switch s {
	case ContainerStateCreated, ContainerStateRunning, ContainerStatePaused,
		ContainerStateRestarting, ContainerStateRemoving, ContainerStateExited,
		ContainerStateDead:
		return true
	default:
		return false
	}
}

// ContainerSpec describes the desired container configuration.
type ContainerSpec struct {
	Image   ImageID           `json:"Image,omitempty"`
	Command []string          `json:"Command,omitempty"`
	Env     map[string]string `json:"Env,omitempty"`
}

// PortBinding describes a host mapping for one container port and protocol.
type PortBinding struct {
	ContainerPort uint16 `json:"ContainerPort"`
	Protocol      string `json:"Protocol"`
	HostIP        string `json:"HostIP,omitempty"`
	HostPort      uint16 `json:"HostPort,omitempty"`
}

// NetworkAttachment describes a container's endpoint on one Docker network.
type NetworkAttachment struct {
	NetworkID           NetworkID `json:"NetworkID"`
	Name                string    `json:"Name,omitempty"`
	EndpointID          string    `json:"EndpointID,omitempty"`
	IPAddress           string    `json:"IPAddress,omitempty"`
	IPPrefixLen         int       `json:"IPPrefixLen,omitempty"`
	Gateway             string    `json:"Gateway,omitempty"`
	GlobalIPv6Address   string    `json:"GlobalIPv6Address,omitempty"`
	GlobalIPv6PrefixLen int       `json:"GlobalIPv6PrefixLen,omitempty"`
	MACAddress          string    `json:"MacAddress,omitempty"`
	Aliases             []string  `json:"Aliases,omitempty"`
}

// ExecRecord stores the inspect-visible state of an exec process.
type ExecRecord struct {
	ID          string      `json:"ID"`
	ContainerID ContainerID `json:"ContainerID"`
	Running     bool        `json:"Running"`
	ExitCode    *int        `json:"ExitCode,omitempty"`
	Command     []string    `json:"Command,omitempty"`
	TTY         bool        `json:"TTY"`
	StartedAt   time.Time   `json:"StartedAt,omitzero"`
	FinishedAt  time.Time   `json:"FinishedAt,omitzero"`
	OpenStdin   bool        `json:"OpenStdin"`
	OpenStdout  bool        `json:"OpenStdout"`
	OpenStderr  bool        `json:"OpenStderr"`
	CanRemove   bool        `json:"CanRemove"`
	ProcessID   int         `json:"Pid,omitempty"`
}

// HealthStatus is the inspect-visible healthcheck status.
type HealthStatus string

const (
	// HealthStatusNone means no healthcheck is configured.
	HealthStatusNone HealthStatus = "none"
	// HealthStatusStarting means the healthcheck has not completed successfully yet.
	HealthStatusStarting HealthStatus = "starting"
	// HealthStatusHealthy means the healthcheck is passing.
	HealthStatusHealthy HealthStatus = "healthy"
	// HealthStatusUnhealthy means the healthcheck is failing.
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// HealthCheckResult is one recorded healthcheck probe result.
type HealthCheckResult struct {
	Start    time.Time `json:"Start"`
	End      time.Time `json:"End"`
	ExitCode int       `json:"ExitCode"`
	Output   string    `json:"Output,omitempty"`
}

// Health stores inspect-visible healthcheck information.
type Health struct {
	Status        HealthStatus        `json:"Status"`
	FailingStreak int                 `json:"FailingStreak"`
	Log           []HealthCheckResult `json:"Log,omitempty"`
}

// Container is the aggregate root for container lifecycle state.
type Container struct {
	ID             ContainerID         `json:"Id"`
	Name           string              `json:"Name"`
	Spec           ContainerSpec       `json:"Spec"`
	ImageReference ImageID             `json:"ImageReference,omitempty"`
	ImageDigest    ImageID             `json:"ImageDigest,omitempty"`
	Labels         map[string]string   `json:"Labels,omitempty"`
	State          ContainerState      `json:"State"`
	CreatedAt      time.Time           `json:"CreatedAt"`
	UpdatedAt      time.Time           `json:"UpdatedAt"`
	ExitCode       int                 `json:"ExitCode"`
	OOMKilled      bool                `json:"OOMKilled"`
	Dead           bool                `json:"Dead"`
	Error          string              `json:"Error,omitempty"`
	StartedAt      time.Time           `json:"StartedAt,omitzero"`
	FinishedAt     time.Time           `json:"FinishedAt,omitzero"`
	Health         *Health             `json:"Health,omitempty"`
	Execs          []ExecRecord        `json:"Execs,omitempty"`
	PortBindings   []PortBinding       `json:"PortBindings,omitempty"`
	Networks       []NetworkAttachment `json:"Networks,omitempty"`
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
	// EventContainerExited records a task exit observed for a container.
	EventContainerExited EventType = "container.exited"
	// EventContainerPaused records a task pause observed for a container.
	EventContainerPaused EventType = "container.paused"
	// EventContainerResumed records a task resume observed for a container.
	EventContainerResumed EventType = "container.resumed"
	// EventContainerDestroyed records deletion of a container.
	EventContainerDestroyed EventType = "container.destroyed"
	// EventExecCreated records creation of an exec process.
	EventExecCreated EventType = "exec.created"
	// EventExecStarted records the start of an exec process.
	EventExecStarted EventType = "exec.started"
	// EventImagePulled records creation of an image record.
	EventImagePulled EventType = "image.pulled"
	// EventImageUntagged records deletion of an image record.
	EventImageUntagged EventType = "image.untagged"
)

// Event records a state transition without coupling the domain to a broker.
type Event struct {
	Type        EventType
	ContainerID ContainerID
	Topic       string
	Action      DockerEventAction
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
