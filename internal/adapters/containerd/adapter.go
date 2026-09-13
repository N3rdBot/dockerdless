// Package containerd provides the containerd runtime adapter.
package containerd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// Containerd labels owned by the adapter. They record Docker semantics that
// are not representable in the OCI spec so Start can reproduce them.
const (
	// labelName stores the Docker container name.
	labelName = "io.dockerdless.name"
	// labelTerminal records whether the container allocates a TTY.
	labelTerminal = "io.dockerdless.terminal"
	// labelOpenStdin records whether the container wants an open stdin.
	labelOpenStdin = "io.dockerdless.open-stdin"
)

// identityPattern constrains operator-supplied IDs, names, and exec IDs.
var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// Adapter is the containerd implementation of the runtime port. It translates
// Docker-shaped lifecycle calls into containerd metadata and task operations.
type Adapter struct {
	client      Client
	snapshotter string
	namespace   string

	stdinMu sync.Mutex
	stdin   map[domain.ContainerID]*io.PipeWriter

	execMu sync.Mutex
	execs  map[string]domain.ExecRecord
}

// Option customizes the adapter.
type Option func(*Adapter)

// WithNamespace pins the containerd namespace used for runtime operations.
func WithNamespace(name string) Option {
	return func(a *Adapter) {
		a.namespace = name
	}
}

// New constructs a containerd runtime adapter around an existing client.
func New(client *containerdclient.Client, opts ...Option) *Adapter {
	return NewWithClient(newContainerdClient(client), opts...)
}

// NewWithClient constructs the adapter around the narrow Client seam so tests
// can inject a fake without a containerd daemon.
func NewWithClient(client Client, opts ...Option) *Adapter {
	a := &Adapter{
		client:    client,
		namespace: namespaces.Default,
		stdin:     make(map[domain.ContainerID]*io.PipeWriter),
		execs:     make(map[string]domain.ExecRecord),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create implements ports.Runtime. It maps the minimal domain spec onto the
// adapter's full Docker configuration and delegates to CreateContainer.
func (a *Adapter) Create(ctx context.Context, spec domain.ContainerSpec) (domain.ContainerID, error) {
	return a.CreateContainer(ctx, Config{
		Image:       spec.Image,
		Command:     spec.Command,
		Env:         spec.Env,
		Snapshotter: a.snapshotter,
	})
}

// CreateContainer resolves the image, unpacks it when needed, translates the
// Docker configuration into an OCI spec, and persists the container metadata
// plus its writable snapshot. The returned ID is the caller's ID when one was
// provided, otherwise a generated 64-character Docker-compatible hex ID.
func (a *Adapter) CreateContainer(ctx context.Context, cfg Config) (domain.ContainerID, error) {
	ctx = a.namespaceContext(ctx)
	if err := validateIdentity(cfg); err != nil {
		return "", err
	}
	if strings.TrimSpace(string(cfg.Image)) == "" {
		return "", fmt.Errorf("%w: image reference is required", ErrInvalidArgument)
	}
	if cfg.ID == "" {
		id, err := newID()
		if err != nil {
			return "", fmt.Errorf("generate container id: %w", err)
		}
		cfg.ID = id
	}
	if cfg.Snapshotter == "" {
		cfg.Snapshotter = a.snapshotter
	}
	if cfg.Name != "" {
		if _, err := a.client.FindContainerByName(ctx, cfg.Name); err == nil {
			return "", fmt.Errorf("container name %q is already in use: %w", cfg.Name, ErrConflict)
		} else if !errdefs.IsNotFound(err) {
			return "", mapError(err)
		}
	}
	image, err := a.client.GetImage(ctx, string(cfg.Image))
	if err != nil {
		return "", mapError(err)
	}
	if err := ensureUnpacked(ctx, image, cfg.Snapshotter); err != nil {
		return "", err
	}
	imageSpec, err := image.Spec(ctx)
	if err != nil {
		return "", mapError(err)
	}
	spec, err := buildSpec(ctx, cfg, imageSpec, a.namespace)
	if err != nil {
		return "", err
	}
	if _, err := a.client.NewContainer(ctx, NewContainerRequest{
		ID:          cfg.ID,
		Image:       image,
		Snapshotter: cfg.Snapshotter,
		SnapshotID:  cfg.SnapshotID,
		Spec:        spec,
		Labels:      containerLabels(cfg),
	}); err != nil {
		return "", mapError(err)
	}
	return domain.ContainerID(cfg.ID), nil
}

// Start implements ports.Runtime by starting a container with detached IO.
func (a *Adapter) Start(ctx context.Context, id domain.ContainerID) error {
	return a.StartWithOptions(ctx, id, StartOptions{})
}

// StartOptions describes the streams attached when a container starts.
type StartOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// StartWithOptions creates and starts the container task after loading the
// existing task (if any) so the caller can restart an exited container.
//
// Deliberate divergence from nerdctl: a container created with OpenStdin but
// without a TTY may be started detached. nerdctl requires a TTY to keep stdin
// interactive. Here the adapter opens a pipe whose read side feeds the task
// stdin and exposes the write side through Stdin, so a later attach can stream
// bytes into the detached container without allocating a terminal.
func (a *Adapter) StartWithOptions(ctx context.Context, id domain.ContainerID, opts StartOptions) error {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return err
	}
	labels, err := container.Labels(ctx)
	if err != nil {
		return mapError(err)
	}
	terminal := labels[labelTerminal] == "true"
	openStdin := labels[labelOpenStdin] == "true"

	if err := discardStaleTask(ctx, id, container); err != nil {
		return err
	}
	task, err := container.NewTask(ctx, a.startTaskIO(id, opts, terminal, openStdin))
	if err != nil {
		a.releaseStdin(id)
		return mapError(err)
	}
	return a.waitAndStartTask(ctx, id, task)
}

// discardStaleTask removes a task left behind by an exited container; a task
// that is still running conflicts.
func discardStaleTask(ctx context.Context, id domain.ContainerID, container Container) error {
	existing, err := container.Task(ctx)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return mapError(err)
	}
	status, err := existing.Status(ctx)
	if err != nil {
		return mapError(err)
	}
	switch status.Status {
	case containerdclient.Running, containerdclient.Paused, containerdclient.Pausing:
		return fmt.Errorf("container %s is already running: %w", id, ErrConflict)
	}
	if _, err := existing.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return mapError(err)
	}
	return nil
}

// startTaskIO builds the task streams, opening a detached stdin pipe when the
// container asked for an interactive stdin without a TTY.
func (a *Adapter) startTaskIO(id domain.ContainerID, opts StartOptions, terminal, openStdin bool) TaskIO {
	streams := TaskIO{Terminal: terminal, Stdin: opts.Stdin, Stdout: opts.Stdout, Stderr: opts.Stderr}
	if streams.Stdin == nil && streams.Stdout == nil && streams.Stderr == nil && openStdin && !terminal {
		reader, writer := io.Pipe()
		a.rememberStdin(id, writer)
		streams.Stdin = reader
	}
	return streams
}

// waitAndStartTask registers the exit watch before starting the task so the
// exit event is not missed, releasing the stdin pipe on any failure.
func (a *Adapter) waitAndStartTask(ctx context.Context, id domain.ContainerID, task Task) error {
	if _, err := task.Wait(ctx); err != nil {
		_, _ = task.Delete(ctx)
		a.releaseStdin(id)
		return mapError(err)
	}
	if err := task.Start(ctx); err != nil {
		_, _ = task.Delete(ctx)
		a.releaseStdin(id)
		return mapError(err)
	}
	return nil
}

// Stop sends SIGTERM and escalates to SIGKILL when the container has not
// exited after timeout. A non-positive timeout skips SIGTERM. Stopping a
// container with no task is a no-op so repeated stops stay idempotent.
func (a *Adapter) Stop(ctx context.Context, id domain.ContainerID, timeout time.Duration) error {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return err
	}
	task, done, err := a.stoppableTask(ctx, id, container)
	if done || err != nil {
		return err
	}
	exitCh, err := task.Wait(ctx)
	if err != nil {
		return mapError(err)
	}
	if timeout > 0 {
		exited, err := a.terminateWithTimeout(ctx, id, task, exitCh, timeout)
		if exited || err != nil {
			return err
		}
	}
	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		return mapError(err)
	}
	return a.waitForExit(ctx, id, exitCh)
}

// stoppableTask loads the task to stop, reporting done when the container has
// no task or its task already stopped.
func (a *Adapter) stoppableTask(ctx context.Context, id domain.ContainerID, container Container) (Task, bool, error) {
	task, err := container.Task(ctx)
	if err != nil {
		if errdefs.IsNotFound(err) {
			a.releaseStdin(id)
			return nil, true, nil
		}
		return nil, false, mapError(err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return nil, false, mapError(err)
	}
	switch status.Status {
	case containerdclient.Created, containerdclient.Stopped:
		if err := task.CloseIO(ctx); err != nil && !errdefs.IsNotFound(err) {
			return nil, false, mapError(err)
		}
		a.releaseStdin(id)
		return nil, true, nil
	}
	return task, false, nil
}

// terminateWithTimeout sends SIGTERM and waits for the task to exit, reporting
// whether it exited before the timeout.
func (a *Adapter) terminateWithTimeout(ctx context.Context, id domain.ContainerID, task Task, exitCh <-chan containerdclient.ExitStatus, timeout time.Duration) (bool, error) {
	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		return false, mapError(err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exitCh:
		a.releaseStdin(id)
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, nil
	}
}

// Kill sends an arbitrary POSIX signal to a running container.
func (a *Adapter) Kill(ctx context.Context, id domain.ContainerID, signal syscall.Signal) error {
	ctx = a.namespaceContext(ctx)
	if signal <= 0 {
		return fmt.Errorf("%w: signal must be positive", ErrInvalidArgument)
	}
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return err
	}
	task, err := container.Task(ctx)
	if err != nil {
		return mapError(err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return mapError(err)
	}
	switch status.Status {
	case containerdclient.Running, containerdclient.Paused, containerdclient.Pausing:
	default:
		return fmt.Errorf("container %s is not running: %w", id, ErrConflict)
	}
	return mapError(task.Kill(ctx, signal))
}

// WaitResult reports how a container task exited.
type WaitResult struct {
	// ExitCode is the process exit code.
	ExitCode uint32
	// ExitedAt is the runtime-reported exit time.
	ExitedAt time.Time
}

// Wait blocks until the container task exits and returns its exit code. A
// nonzero exit code is data, not an error.
func (a *Adapter) Wait(ctx context.Context, id domain.ContainerID) (WaitResult, error) {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return WaitResult{}, err
	}
	task, err := container.Task(ctx)
	if err != nil {
		return WaitResult{}, mapError(err)
	}
	exitCh, err := task.Wait(ctx)
	if err != nil {
		return WaitResult{}, mapError(err)
	}
	select {
	case <-ctx.Done():
		return WaitResult{}, ctx.Err()
	case exit, ok := <-exitCh:
		if !ok {
			return WaitResult{}, fmt.Errorf("wait for container %s: exit channel closed: %w", id, ErrServerError)
		}
		code, exitedAt, err := exit.Result()
		if err != nil {
			return WaitResult{}, mapError(err)
		}
		a.releaseStdin(id)
		return WaitResult{ExitCode: code, ExitedAt: exitedAt}, nil
	}
}

// Remove deletes the task, then the container metadata, then the snapshot, in
// that order. Missing tasks, containers, and snapshots are ignored so repeated
// removals stay idempotent. Deleting a container with a running task conflicts,
// matching Docker's behavior without a force flag.
func (a *Adapter) Remove(ctx context.Context, id domain.ContainerID) error {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		// The container record is the only place the per-container snapshotter
		// name is stored, so once it is gone the snapshotter is unknowable.
		// Guessing one here (resolving the empty name) risks removing from the
		// wrong snapshotter, so a missing container is treated as already
		// removed; an orphaned snapshot left by an interrupted removal is
		// reaped by the external sweep.
		a.releaseStdin(id)
		return nil
	}
	task, err := container.Task(ctx)
	if err == nil {
		if _, err := task.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
			return mapError(err)
		}
	} else if !errdefs.IsNotFound(err) {
		return mapError(err)
	}
	if err := container.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return mapError(err)
	}
	a.releaseStdin(id)
	snapshotter, key := container.Snapshot()
	if key == "" {
		return nil
	}
	return a.removeSnapshot(ctx, snapshotter, key)
}

// Status reports the Docker lifecycle state. Running state is derived from the
// runtime task, never from container metadata alone: a container without a
// task is reported as exited.
func (a *Adapter) Status(ctx context.Context, id domain.ContainerID) (domain.ContainerState, error) {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return domain.ContainerStateUnknown, err
	}
	task, err := container.Task(ctx)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return domain.ContainerStateExited, nil
		}
		return domain.ContainerStateUnknown, mapError(err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return domain.ContainerStateUnknown, mapError(err)
	}
	switch status.Status {
	case containerdclient.Running:
		return domain.ContainerStateRunning, nil
	case containerdclient.Paused:
		return domain.ContainerStatePaused, nil
	case containerdclient.Created:
		return domain.ContainerStateCreated, nil
	case containerdclient.Stopped:
		return domain.ContainerStateExited, nil
	default:
		return domain.ContainerStateUnknown, nil
	}
}

// Resize resizes the terminal of a running container task.
func (a *Adapter) Resize(ctx context.Context, id domain.ContainerID, width, height uint32) error {
	ctx = a.namespaceContext(ctx)
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return err
	}
	task, err := container.Task(ctx)
	if err != nil {
		return mapError(err)
	}
	return mapError(task.Resize(ctx, width, height))
}

// Stdin returns the write side of the detached interactive stdin pipe created
// for a container started with OpenStdin but no TTY.
func (a *Adapter) Stdin(id domain.ContainerID) (io.WriteCloser, bool) {
	a.stdinMu.Lock()
	defer a.stdinMu.Unlock()
	writer, ok := a.stdin[id]
	if !ok {
		return nil, false
	}
	return writer, true
}

// loadContainer resolves a container through the narrow seam.
func (a *Adapter) loadContainer(ctx context.Context, id domain.ContainerID) (Container, error) {
	container, err := a.client.LoadContainer(ctx, string(id))
	if err != nil {
		return nil, mapError(err)
	}
	return container, nil
}

// namespaceContext ensures containerd calls carry the adapter namespace, which
// the OCI default spec also needs for the cgroups path.
func (a *Adapter) namespaceContext(ctx context.Context) context.Context {
	if _, ok := namespaces.Namespace(ctx); ok {
		return ctx
	}
	return namespaces.WithNamespace(ctx, a.namespace)
}

// rememberStdin stores the write side of a detached stdin pipe, closing any
// previous pipe for the same container.
func (a *Adapter) rememberStdin(id domain.ContainerID, writer *io.PipeWriter) {
	a.stdinMu.Lock()
	previous := a.stdin[id]
	a.stdin[id] = writer
	a.stdinMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

// releaseStdin closes the detached stdin pipe for a container.
func (a *Adapter) releaseStdin(id domain.ContainerID) {
	a.stdinMu.Lock()
	writer := a.stdin[id]
	delete(a.stdin, id)
	a.stdinMu.Unlock()
	if writer != nil {
		_ = writer.Close()
	}
}

// waitForExit blocks until the task reports exit or the context ends.
func (a *Adapter) waitForExit(ctx context.Context, id domain.ContainerID, exitCh <-chan containerdclient.ExitStatus) error {
	select {
	case <-exitCh:
		a.releaseStdin(id)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// removeSnapshot deletes a snapshot, treating missing snapshots as success.
func (a *Adapter) removeSnapshot(ctx context.Context, snapshotter, key string) error {
	if err := a.client.RemoveSnapshot(ctx, snapshotter, key); err != nil && !errdefs.IsNotFound(err) {
		return mapError(err)
	}
	return nil
}

// ensureUnpacked makes sure the image layers are unpacked for the snapshotter
// before a writable snapshot is created from them.
func ensureUnpacked(ctx context.Context, image Image, snapshotter string) error {
	unpacked, err := image.IsUnpacked(ctx, snapshotter)
	if err != nil {
		return mapError(err)
	}
	if unpacked {
		return nil
	}
	if err := image.Unpack(ctx, snapshotter); err != nil {
		return mapError(err)
	}
	return nil
}

// containerLabels records the Docker metadata Start needs later.
func containerLabels(cfg Config) map[string]string {
	labels := make(map[string]string, 3)
	if cfg.Name != "" {
		labels[labelName] = cfg.Name
	}
	if cfg.Terminal {
		labels[labelTerminal] = "true"
	}
	if cfg.OpenStdin {
		labels[labelOpenStdin] = "true"
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// validateIdentity rejects IDs and names that cannot be container identities.
func validateIdentity(cfg Config) error {
	if cfg.ID != "" && !identityPattern.MatchString(cfg.ID) {
		return fmt.Errorf("%w: invalid container id %q", ErrInvalidArgument, cfg.ID)
	}
	if cfg.Name != "" && !identityPattern.MatchString(cfg.Name) {
		return fmt.Errorf("%w: invalid container name %q", ErrInvalidArgument, cfg.Name)
	}
	return nil
}

// newID generates a Docker-compatible 64-character lowercase hex identity.
func newID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var _ ports.Runtime = (*Adapter)(nil)
