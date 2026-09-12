// Package ports defines the inward-facing contracts used by dockerdless.
package ports

import (
	"context"
	"errors"
	"io"
	"syscall"
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

// Canonical transport-neutral errors. Adapters wrap these sentinels from their
// own failure vocabulary, so the HTTP boundary maps failures to Docker status
// codes without importing any infrastructure package.
var (
	// ErrNotFound reports an absent container, image, network, or exec.
	ErrNotFound = errors.New("dockerdless: not found")
	// ErrConflict reports a request that conflicts with current state.
	ErrConflict = errors.New("dockerdless: conflict")
	// ErrInvalidArgument reports a malformed request the daemon rejects.
	ErrInvalidArgument = errors.New("dockerdless: invalid argument")
	// ErrNotImplemented reports a recognized operation this MVP cannot serve.
	ErrNotImplemented = errors.New("dockerdless: not implemented")
	// ErrServerError reports an unexpected backend or daemon failure.
	ErrServerError = errors.New("dockerdless: server failure")
)

// LabelTTY is the internal container label recording terminal allocation. The
// HTTP layer reads it for log framing and filters it from inspect output.
const LabelTTY = "io.dockerdless.tty"

// RegistryAuth carries Docker registry credentials in a transport-neutral
// shape. It is converted to the BuildKit adapter's redacting type at the
// application boundary and is never logged.
type RegistryAuth struct {
	Username      string
	Password      string
	IdentityToken string
	RegistryToken string
	ServerAddress string
}

// PullRequest configures one image pull.
type PullRequest struct {
	// Reference is the image reference to pull.
	Reference string
	// Platform is an OCI platform string such as "linux/amd64".
	Platform string
	// Auth optionally carries registry credentials.
	Auth *RegistryAuth
}

// SystemStatus summarizes daemon-wide counters and backend identity.
type SystemStatus struct {
	// Containers, ContainersRunning, ContainersPaused, and ContainersStopped
	// are the Docker container counters.
	Containers        int
	ContainersRunning int
	ContainersPaused  int
	ContainersStopped int
	// Images is the number of stored image records.
	Images int
	// Namespace is the active containerd namespace.
	Namespace string
	// ContainerdSocket and BuildKitSocket are the configured backend sockets.
	ContainerdSocket string
	BuildKitSocket   string
	// Snapshotter is the active containerd snapshotter.
	Snapshotter string
	// Warnings are informational startup warnings surfaced through /info.
	Warnings []string
}

// PortReservation is one reserved host port.
type PortReservation struct {
	// Protocol is "tcp" or "udp".
	Protocol string
	// HostIP is the normalized bind address.
	HostIP string
	// HostPort is the reserved concrete port; it is never zero.
	HostPort uint16
}

// PortAllocator reserves concrete host ports before CNI port mapping runs.
type PortAllocator interface {
	// AllocateBindings gives every binding a concrete reservation, rolling
	// back every reservation when one binding fails.
	AllocateBindings([]domain.PortBinding) ([]domain.PortBinding, []PortReservation, error)
	// Release frees previously allocated ports.
	Release(...PortReservation)
}

// ContainerCreateSpec is the full container configuration the runtime needs.
type ContainerCreateSpec struct {
	// ID optionally pins the container identity.
	ID string
	// Name is the Docker container name.
	Name string
	// Image is the image reference the container is created from.
	Image domain.ImageID
	// Command overrides the image CMD.
	Command []string
	// Entrypoint overrides the image ENTRYPOINT.
	Entrypoint []string
	// Env adds or replaces image environment variables.
	Env map[string]string
	// WorkingDir overrides the image working directory.
	WorkingDir string
	// User overrides the image user.
	User string
	// Hostname sets the container hostname.
	Hostname string
	// Labels become container labels.
	Labels map[string]string
	// Mounts are bind and tmpfs mounts.
	Mounts []Mount
	// TTY allocates a terminal for the container process.
	TTY bool
	// OpenStdin keeps stdin open.
	OpenStdin bool
	// Snapshotter optionally overrides the container snapshotter.
	Snapshotter string
}

// ImageDetail is the daemon-neutral projection of one stored image.
type ImageDetail struct {
	// ID is the Docker image ID (the image config digest).
	ID domain.ImageID
	// RepoTags and RepoDigests are the familiar references of the image.
	RepoTags    []string
	RepoDigests []string
	// Platform is the image platform such as "linux/amd64".
	Platform string
	// Size is the packed image size in bytes.
	Size int64
	// Created is the image creation time.
	Created time.Time
	// Labels are the image labels.
	Labels map[string]string
}

// BuildRequest is one Docker POST /build request decoded into a daemon-neutral
// form.
type BuildRequest struct {
	// Context is the tar archive of the build context (the request body).
	Context io.Reader
	// Tag is the image reference to apply.
	Tag string
	// Dockerfile is the Dockerfile path inside the context.
	Dockerfile string
	// Platform selects the build platform.
	Platform string
	// BuildArgs are Dockerfile build arguments.
	BuildArgs map[string]string
	// Target selects a build stage.
	Target string
	// NoCache disables BuildKit cache reuse.
	NoCache bool
	// Auth optionally carries registry credentials for base image pulls.
	Auth *RegistryAuth
}

// Mount describes one bind or tmpfs mount on a container.
type Mount struct {
	// Type is the mount type: "bind" (default) or "tmpfs".
	Type string
	// Source is the host path for bind mounts.
	Source string
	// Destination is the absolute container path.
	Destination string
	// ReadOnly mounts the source read-only.
	ReadOnly bool
	// Options are additional mount options.
	Options []string
}

// ContainerCreateRequest is the daemon-neutral form of POST /containers/create.
type ContainerCreateRequest struct {
	// Name is the Docker container name without a leading slash.
	Name string
	// Image is the operator's image reference.
	Image string
	// Command overrides the image CMD.
	Command []string
	// Entrypoint overrides the image ENTRYPOINT.
	Entrypoint []string
	// Env are environment variable overrides.
	Env map[string]string
	// WorkingDir overrides the image working directory.
	WorkingDir string
	// User overrides the image user (numeric uid[:gid]).
	User string
	// Hostname overrides the container hostname.
	Hostname string
	// Labels are Docker labels.
	Labels map[string]string
	// TTY allocates a terminal for the container process.
	TTY bool
	// OpenStdin keeps stdin open.
	OpenStdin bool
	// NetworkMode is the requested Docker network mode or network name.
	NetworkMode string
	// PortBindings are the requested published ports; a zero HostPort asks for
	// a dynamic allocation.
	PortBindings []domain.PortBinding
	// Mounts are bind and tmpfs mounts.
	Mounts []Mount
}

// ContainerCreateResult reports the created Docker identity.
type ContainerCreateResult struct {
	// ID is the Docker container identity.
	ID domain.ContainerID
	// Warnings are Docker create-time warnings.
	Warnings []string
}

// ProcessExit reports how a container task exited.
type ProcessExit struct {
	// ExitCode is the process exit code.
	ExitCode uint32
	// ExitedAt is the runtime-reported exit time.
	ExitedAt time.Time
}

// ExecRequest describes a process to run inside a running container.
type ExecRequest struct {
	// ID optionally overrides the generated exec identity.
	ID string
	// Command is the process argument vector; it is required.
	Command []string
	// Env adds or replaces environment variables of the exec process.
	Env map[string]string
	// WorkingDir overrides the container working directory.
	WorkingDir string
	// User overrides the container user (numeric uid[:gid] only).
	User string
	// TTY allocates a terminal for the exec process.
	TTY bool
	// Detach starts the process and returns without waiting for exit.
	Detach bool
	// Width and Height resize a terminal process after start.
	Width  uint32
	Height uint32
	// Stdin, Stdout, and Stderr attach caller streams to the process.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// ExecResult reports one started exec process.
type ExecResult struct {
	// ID is the exec process identity.
	ID string
	// ExitCode is set once the process has exited.
	ExitCode *int
	// Running reports whether the process is still running.
	Running bool
	// Pid is the process ID inside the container.
	Pid int
	// StartedAt and FinishedAt bracket the process lifetime.
	StartedAt  time.Time
	FinishedAt time.Time
}

// ExecCreateRequest is the daemon-neutral form of POST /containers/{id}/exec.
type ExecCreateRequest struct {
	Command      []string
	Env          map[string]string
	WorkingDir   string
	User         string
	TTY          bool
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
}

// ExecStartRequest starts a previously created exec process. Stdin/Stdout/
// Stderr carry the hijacked connection for attached execs. OnResize is invoked
// for every TTY resize message decoded from the input stream.
type ExecStartRequest struct {
	// Detach starts the process without attaching streams.
	Detach bool
	// TTY mirrors the exec-create TTY flag.
	TTY bool
	// Width and Height are the initial terminal size when known.
	Width  uint
	Height uint
	// Stdin, Stdout, and Stderr hold the attachment streams.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// OnResize receives TTY resize messages; it may be nil.
	OnResize func(width, height uint)
}

// LogRequest filters one container log stream.
type LogRequest struct {
	// Follow keeps streaming after EOF.
	Follow bool
	// Tail limits the stream to the last N complete lines; 0 means all.
	Tail int
	// Since drops entries older than this time.
	Since time.Time
	// Until stops at the first entry newer than this time.
	Until time.Time
	// Timestamps prefixes entries with their RFC3339Nano time.
	Timestamps bool
	// Stdout and Stderr select which streams are emitted.
	Stdout bool
	Stderr bool
}

// NetworkDetail is the daemon-neutral projection of one Docker network.
type NetworkDetail struct {
	// ID is the deterministic Docker network identity.
	ID domain.NetworkID
	// Name is the Docker network name.
	Name string
	// Driver is the network driver such as "bridge".
	Driver string
	// Mode is the classification: "network", "host", or "none".
	Mode string
	// Subnet and Gateway describe the IPv4 allocation when known.
	Subnet  string
	Gateway string
	// Labels are the Docker network labels.
	Labels map[string]string
}

// NetworkCreateRequest is the daemon-neutral form of POST /networks/create.
type NetworkCreateRequest struct {
	Name       string
	Driver     string
	EnableIPv6 bool
	Labels     map[string]string
}

// NetworkCreateResult reports a created network.
type NetworkCreateResult struct {
	// ID is the deterministic Docker network identity.
	ID domain.NetworkID
	// Warning is Docker's create-time warning, empty on success.
	Warning string
}

// NetworkConnectRequest describes attaching a container to a network. NetNS
// is required for named networks; Ports carries already-reserved concrete
// bindings and must never contain host port zero.
type NetworkConnectRequest struct {
	Network   string
	Container domain.ContainerID
	NetNS     string
	IfName    string
	Ports     []domain.PortBinding
	Aliases   []string
}

// NetworkAttachmentResult reports the attached endpoint and the concrete port
// allocations configured in CNI portmap.
type NetworkAttachmentResult struct {
	// Attachment is the Docker-facing endpoint.
	Attachment domain.NetworkAttachment
	// Ports are concrete bindings; HostPort is never zero.
	Ports []domain.PortBinding
}

// RuntimeController extends Runtime with every operation the API handlers
// need. The containerd adapter satisfies the lifecycle subset directly; the
// adapter-local result types (WaitResult, ExecConfig) are normalized by the
// application layer.
type RuntimeController interface {
	Runtime
	// CreateContainer creates a container from the full daemon-neutral spec,
	// including Docker name, TTY, stdin, labels, and mounts.
	CreateContainer(context.Context, ContainerCreateSpec) (domain.ContainerID, error)
	// Kill sends an arbitrary POSIX signal to a running container.
	Kill(context.Context, domain.ContainerID, syscall.Signal) error
	// Wait blocks until the container task exits.
	Wait(context.Context, domain.ContainerID) (ProcessExit, error)
	// Status reports the Docker lifecycle state of a container.
	Status(context.Context, domain.ContainerID) (domain.ContainerState, error)
	// Resize resizes the terminal of a running container task.
	Resize(context.Context, domain.ContainerID, uint32, uint32) error
	// StartWithIO starts a container and attaches stdout/stderr writers so the
	// daemon can capture logs. A nil writer detaches that stream.
	StartWithIO(context.Context, domain.ContainerID, io.Writer, io.Writer) error
	// Exec creates, starts, and (unless detached) waits for a process.
	Exec(context.Context, domain.ContainerID, ExecRequest) (ExecResult, error)
	// ExecRecord returns the recorded state of one exec process.
	ExecRecord(string) (domain.ExecRecord, bool)
}

// ImageController extends Image with the inspect, list, and build operations
// the API handlers need.
type ImageController interface {
	Image
	// PullImage pulls with explicit platform and registry-auth options.
	PullImage(context.Context, string, PullRequest) (domain.ImageID, error)
	// Inspect resolves a reference or ID and returns its detail projection.
	Inspect(context.Context, string) (ImageDetail, error)
	// List returns every stored image.
	List(context.Context) ([]ImageDetail, error)
	// Build runs a Dockerfile build and writes Docker JSON progress to out.
	Build(context.Context, BuildRequest, io.Writer) error
}

// NetworkController extends Network with the list, resolve, and connect
// operations the API handlers need.
type NetworkController interface {
	Network
	// EnsureDefaultNetwork creates Docker's default bridge network when missing.
	EnsureDefaultNetwork(context.Context) (domain.NetworkID, error)
	// Resolve finds a network by name, full ID, or ID prefix.
	Resolve(context.Context, string) (NetworkDetail, error)
	// List returns every configured network.
	List(context.Context) ([]NetworkDetail, error)
	// CreateNetwork creates a bridge-backed network.
	CreateNetwork(context.Context, NetworkCreateRequest) (domain.NetworkID, error)
	// Connect attaches a container to a network and returns the concrete ports.
	Connect(context.Context, NetworkConnectRequest) (NetworkAttachmentResult, error)
	// DisconnectAll tears down every attachment of a container.
	DisconnectAll(context.Context, domain.ContainerID) []error
}

// TaskLocator resolves the host PID of a running container task so the network
// adapter can be pointed at the container network namespace after start.
type TaskLocator interface {
	TaskPID(context.Context, domain.ContainerID) (int, error)
}

// Logs streams a container's recorded logs.
type Logs interface {
	Logs(context.Context, domain.ContainerID, LogRequest, io.Writer, io.Writer) error
}
