package containerd

import (
	"context"
	"fmt"
	"io"
	"syscall"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// Client is the narrow seam over the containerd operations the runtime adapter
// needs. Production code wraps a live *containerdclient.Client; tests inject a
// fake so lifecycle behavior is verified without a daemon.
//
// The seam deliberately avoids containerd option funcs and concrete client
// types in its signatures so a fake never has to emulate containerd internals.
type Client interface {
	// GetImage resolves an image reference in the current namespace.
	GetImage(ctx context.Context, ref string) (Image, error)
	// NewContainer creates container metadata plus its writable snapshot.
	NewContainer(ctx context.Context, req NewContainerRequest) (Container, error)
	// LoadContainer loads existing container metadata by ID.
	LoadContainer(ctx context.Context, id string) (Container, error)
	// FindContainerByName looks up a container by its Docker name label.
	FindContainerByName(ctx context.Context, name string) (Container, error)
	// RemoveSnapshot removes a snapshot key, ignoring missing snapshots.
	RemoveSnapshot(ctx context.Context, snapshotter, key string) error
}

// Image is the subset of a containerd image the adapter consumes.
type Image interface {
	// Name returns the image reference.
	Name() string
	// Spec returns the resolved OCI image spec (platform matched).
	Spec(ctx context.Context) (ocispec.Image, error)
	// IsUnpacked reports whether the image is unpacked for the snapshotter.
	IsUnpacked(ctx context.Context, snapshotter string) (bool, error)
	// Unpack unpacks the image for the snapshotter.
	Unpack(ctx context.Context, snapshotter string) error
}

// NewContainerRequest captures a metadata plus snapshot container creation.
type NewContainerRequest struct {
	ID          string
	Image       Image
	Snapshotter string
	SnapshotID  string
	Spec        *oci.Spec
	Labels      map[string]string
}

// Container is the subset of a containerd container the adapter consumes.
//
// Containerd splits metadata (Containers service) from execution (Tasks
// service): a container may exist with no task, and only a task can report a
// running process.
type Container interface {
	ID() string
	Labels(ctx context.Context) (map[string]string, error)
	Spec(ctx context.Context) (*oci.Spec, error)
	// NewTask creates and registers a task for the container metadata.
	NewTask(ctx context.Context, streams TaskIO) (Task, error)
	// Task loads the existing task, returning NotFound when none exists.
	Task(ctx context.Context) (Task, error)
	// Delete removes container metadata. It fails with FailedPrecondition
	// while a task still exists.
	Delete(ctx context.Context) error
	// Snapshot reports the snapshotter name and snapshot key of the container.
	Snapshot() (snapshotter, key string)
}

// Task is the subset of a containerd task the adapter consumes.
type Task interface {
	Process
	// Exec registers a new process inside the running task.
	Exec(ctx context.Context, id string, spec *specs.Process, streams TaskIO) (Process, error)
}

// Process is the subset of a containerd process the adapter consumes. Both
// container tasks and exec processes satisfy it.
type Process interface {
	ID() string
	Pid() uint32
	Start(ctx context.Context) error
	Wait(ctx context.Context) (<-chan containerdclient.ExitStatus, error)
	Kill(ctx context.Context, signal syscall.Signal) error
	Resize(ctx context.Context, width, height uint32) error
	CloseIO(ctx context.Context) error
	Delete(ctx context.Context) (*containerdclient.ExitStatus, error)
	Status(ctx context.Context) (containerdclient.Status, error)
}

// TaskIO describes the streams attached to a task or exec process. A zero
// TaskIO with no terminal detaches the process with null IO.
type TaskIO struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Terminal bool
}

// creator converts the stream description into a containerd IO creator.
func (s TaskIO) creator() cio.Creator {
	if s.Stdin == nil && s.Stdout == nil && s.Stderr == nil && !s.Terminal {
		return cio.NullIO
	}
	opts := []cio.Opt{cio.WithStreams(s.Stdin, s.Stdout, s.Stderr)}
	if s.Terminal {
		opts = append(opts, cio.WithTerminal)
	}
	return cio.NewCreator(opts...)
}

// containerdClient implements Client on top of a live containerd client.
type containerdClient struct {
	client *containerdclient.Client
}

// newContainerdClient wraps a live containerd client in the narrow seam.
func newContainerdClient(client *containerdclient.Client) *containerdClient {
	return &containerdClient{client: client}
}

// GetImage resolves an image reference.
func (c *containerdClient) GetImage(ctx context.Context, ref string) (Image, error) {
	image, err := c.client.GetImage(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &containerdImage{image: image}, nil
}

// NewContainer creates container metadata, its writable snapshot, and the
// translated OCI spec in one containerd operation.
func (c *containerdClient) NewContainer(ctx context.Context, req NewContainerRequest) (Container, error) {
	image, ok := req.Image.(*containerdImage)
	if !ok {
		return nil, fmt.Errorf("%w: image %T is not containerd-backed", ErrInvalidArgument, req.Image)
	}
	snapshotID := req.SnapshotID
	if snapshotID == "" {
		snapshotID = req.ID
	}
	opts := []containerdclient.NewContainerOpts{
		containerdclient.WithImage(image.image),
	}
	if req.Snapshotter != "" {
		opts = append(opts, containerdclient.WithSnapshotter(req.Snapshotter))
	}
	opts = append(opts, containerdclient.WithNewSnapshot(snapshotID, image.image))
	if req.Spec != nil {
		opts = append(opts, containerdclient.WithSpec(req.Spec))
	}
	if len(req.Labels) > 0 {
		opts = append(opts, containerdclient.WithContainerLabels(req.Labels))
	}
	container, err := c.client.NewContainer(ctx, req.ID, opts...)
	if err != nil {
		return nil, err
	}
	return c.wrapContainer(ctx, container)
}

// LoadContainer loads container metadata by ID.
func (c *containerdClient) LoadContainer(ctx context.Context, id string) (Container, error) {
	container, err := c.client.LoadContainer(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.wrapContainer(ctx, container)
}

// FindContainerByName finds a container by its Docker name label.
func (c *containerdClient) FindContainerByName(ctx context.Context, name string) (Container, error) {
	containers, err := c.client.Containers(ctx, "labels."+labelName+"=="+name)
	if err != nil {
		return nil, err
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("container name %q: %w", name, errdefs.ErrNotFound)
	}
	return c.wrapContainer(ctx, containers[0])
}

// RemoveSnapshot removes a snapshot, returning NotFound for missing keys.
func (c *containerdClient) RemoveSnapshot(ctx context.Context, snapshotter, key string) error {
	if key == "" {
		return nil
	}
	return c.client.SnapshotService(snapshotter).Remove(ctx, key)
}

// wrapContainer snapshots the container metadata the adapter needs later.
func (c *containerdClient) wrapContainer(ctx context.Context, container containerdclient.Container) (Container, error) {
	info, err := container.Info(ctx, containerdclient.WithoutRefreshedMetadata)
	if err != nil {
		return nil, err
	}
	return &containerdContainer{
		container:   container,
		id:          container.ID(),
		snapshotter: info.Snapshotter,
		snapshotKey: info.SnapshotKey,
	}, nil
}

// containerdImage implements Image over a containerd image handle.
type containerdImage struct {
	image containerdclient.Image
}

// Name returns the image reference.
func (i *containerdImage) Name() string {
	return i.image.Name()
}

// Spec returns the resolved OCI image spec.
func (i *containerdImage) Spec(ctx context.Context) (ocispec.Image, error) {
	return i.image.Spec(ctx)
}

// IsUnpacked reports whether the image is unpacked for the snapshotter.
func (i *containerdImage) IsUnpacked(ctx context.Context, snapshotter string) (bool, error) {
	return i.image.IsUnpacked(ctx, snapshotter)
}

// Unpack unpacks the image for the snapshotter.
func (i *containerdImage) Unpack(ctx context.Context, snapshotter string) error {
	return i.image.Unpack(ctx, snapshotter)
}

// containerdContainer implements Container over a containerd container handle.
type containerdContainer struct {
	container   containerdclient.Container
	id          string
	snapshotter string
	snapshotKey string
}

// ID returns the container identity.
func (c *containerdContainer) ID() string {
	return c.id
}

// Labels returns the containerd container labels.
func (c *containerdContainer) Labels(ctx context.Context) (map[string]string, error) {
	return c.container.Labels(ctx)
}

// Spec returns the stored OCI spec.
func (c *containerdContainer) Spec(ctx context.Context) (*oci.Spec, error) {
	return c.container.Spec(ctx)
}

// NewTask registers a task for the container.
func (c *containerdContainer) NewTask(ctx context.Context, streams TaskIO) (Task, error) {
	task, err := c.container.NewTask(ctx, streams.creator())
	if err != nil {
		return nil, err
	}
	return &containerdTask{task: task}, nil
}

// Task loads the current task, returning NotFound when none is registered.
func (c *containerdContainer) Task(ctx context.Context) (Task, error) {
	task, err := c.container.Task(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &containerdTask{task: task}, nil
}

// Delete removes the container metadata.
func (c *containerdContainer) Delete(ctx context.Context) error {
	return c.container.Delete(ctx)
}

// Snapshot reports the snapshotter name and snapshot key.
func (c *containerdContainer) Snapshot() (string, string) {
	return c.snapshotter, c.snapshotKey
}

// containerdTask implements Task over a containerd task handle.
type containerdTask struct {
	task containerdclient.Task
}

// ID returns the task identity.
func (t *containerdTask) ID() string {
	return t.task.ID()
}

// Pid returns the init process ID.
func (t *containerdTask) Pid() uint32 {
	return t.task.Pid()
}

// Start starts the task init process.
func (t *containerdTask) Start(ctx context.Context) error {
	return t.task.Start(ctx)
}

// Wait returns the exit status channel. It must be called before Start so no
// exit event is missed.
func (t *containerdTask) Wait(ctx context.Context) (<-chan containerdclient.ExitStatus, error) {
	return t.task.Wait(ctx)
}

// Kill sends a signal to the task init process.
func (t *containerdTask) Kill(ctx context.Context, signal syscall.Signal) error {
	return t.task.Kill(ctx, signal)
}

// Resize resizes the task terminal.
func (t *containerdTask) Resize(ctx context.Context, width, height uint32) error {
	return t.task.Resize(ctx, width, height)
}

// CloseIO closes the task IO streams.
func (t *containerdTask) CloseIO(ctx context.Context) error {
	return t.task.CloseIO(ctx)
}

// Delete removes the task and returns its recorded exit status.
func (t *containerdTask) Delete(ctx context.Context) (*containerdclient.ExitStatus, error) {
	return t.task.Delete(ctx)
}

// Status returns the runtime task status.
func (t *containerdTask) Status(ctx context.Context) (containerdclient.Status, error) {
	return t.task.Status(ctx)
}

// Exec registers a new process inside the task.
func (t *containerdTask) Exec(ctx context.Context, id string, spec *specs.Process, streams TaskIO) (Process, error) {
	process, err := t.task.Exec(ctx, id, spec, streams.creator())
	if err != nil {
		return nil, err
	}
	return &containerdProcess{process: process}, nil
}

// containerdProcess implements Process over a containerd process handle.
type containerdProcess struct {
	process containerdclient.Process
}

// ID returns the process identity.
func (p *containerdProcess) ID() string {
	return p.process.ID()
}

// Pid returns the process ID.
func (p *containerdProcess) Pid() uint32 {
	return p.process.Pid()
}

// Start starts the process.
func (p *containerdProcess) Start(ctx context.Context) error {
	return p.process.Start(ctx)
}

// Wait returns the exit status channel. It must be called before Start.
func (p *containerdProcess) Wait(ctx context.Context) (<-chan containerdclient.ExitStatus, error) {
	return p.process.Wait(ctx)
}

// Kill sends a signal to the process.
func (p *containerdProcess) Kill(ctx context.Context, signal syscall.Signal) error {
	return p.process.Kill(ctx, signal)
}

// Resize resizes the process terminal.
func (p *containerdProcess) Resize(ctx context.Context, width, height uint32) error {
	return p.process.Resize(ctx, width, height)
}

// CloseIO closes the process IO streams.
func (p *containerdProcess) CloseIO(ctx context.Context) error {
	return p.process.CloseIO(ctx)
}

// Delete removes the process and returns its recorded exit status.
func (p *containerdProcess) Delete(ctx context.Context) (*containerdclient.ExitStatus, error) {
	return p.process.Delete(ctx)
}

// Status returns the runtime process status.
func (p *containerdProcess) Status(ctx context.Context) (containerdclient.Status, error) {
	return p.process.Status(ctx)
}
