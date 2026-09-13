package containerd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

// ExecConfig describes a process to run inside a running container.
type ExecConfig struct {
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
	// Terminal allocates a TTY for the exec process.
	Terminal bool
	// Stdin, Stdout, and Stderr attach caller streams to the process.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Detach starts the process and returns without waiting for exit.
	Detach bool
	// Width and Height resize a terminal process after start.
	Width  uint32
	Height uint32
}

// ExecResult reports the executed process and, when not detached, its exit
// code.
type ExecResult struct {
	// ID is the exec process identity.
	ID string
	// Pid is the process ID inside the container.
	Pid uint32
	// ExitCode is set once the process has exited.
	ExitCode *int
	// StartedAt and FinishedAt bracket the process lifetime.
	StartedAt  time.Time
	FinishedAt time.Time
	// Detached reports that the caller did not wait for exit.
	Detached bool
}

// Exec creates, starts, and waits for a process inside a running container.
// The exec is recorded in memory so inspect handlers can serve exec state even
// after the process exits. Terminal processes are resized after start; the
// exit code is data, not an error.
func (a *Adapter) Exec(ctx context.Context, id domain.ContainerID, cfg ExecConfig) (ExecResult, error) {
	ctx = a.namespaceContext(ctx)
	if err := validateExecConfig(cfg); err != nil {
		return ExecResult{}, err
	}
	container, task, err := a.runningTask(ctx, id)
	if err != nil {
		return ExecResult{}, err
	}
	containerSpec, err := container.Spec(ctx)
	if err != nil {
		return ExecResult{}, mapError(err)
	}
	processSpec, err := buildExecProcessSpec(ctx, containerSpec.Process, cfg)
	if err != nil {
		return ExecResult{}, err
	}
	execID, err := resolveExecID(cfg.ID)
	if err != nil {
		return ExecResult{}, err
	}
	process, err := task.Exec(ctx, execID, processSpec, TaskIO{
		Stdin:    cfg.Stdin,
		Stdout:   cfg.Stdout,
		Stderr:   cfg.Stderr,
		Terminal: cfg.Terminal,
	})
	if err != nil {
		return ExecResult{}, mapError(err)
	}
	if cfg.Detach {
		return a.execDetached(ctx, id, execID, process, cfg)
	}
	return a.execAttached(ctx, id, execID, process, cfg)
}

// validateExecConfig rejects exec requests that cannot identify a process.
func validateExecConfig(cfg ExecConfig) error {
	if len(cfg.Command) == 0 {
		return fmt.Errorf("%w: exec command is required", ErrInvalidArgument)
	}
	if cfg.ID != "" && !identityPattern.MatchString(cfg.ID) {
		return fmt.Errorf("%w: invalid exec id %q", ErrInvalidArgument, cfg.ID)
	}
	return nil
}

// runningTask loads a container and its task, rejecting non-running tasks.
func (a *Adapter) runningTask(ctx context.Context, id domain.ContainerID) (Container, Task, error) {
	container, err := a.loadContainer(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	task, err := container.Task(ctx)
	if err != nil {
		return nil, nil, mapError(err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return nil, nil, mapError(err)
	}
	if status.Status != containerdclient.Running {
		return nil, nil, fmt.Errorf("container %s is not running: %w", id, ErrConflict)
	}
	return container, task, nil
}

// resolveExecID returns the caller's exec identity or generates a new one.
func resolveExecID(id string) (string, error) {
	if id != "" {
		return id, nil
	}
	execID, err := newID()
	if err != nil {
		return "", fmt.Errorf("generate exec id: %w", err)
	}
	return execID, nil
}

// execDetached starts an exec process and records it as running.
func (a *Adapter) execDetached(ctx context.Context, containerID domain.ContainerID, execID string, process Process, cfg ExecConfig) (ExecResult, error) {
	result := ExecResult{ID: execID, Detached: true}
	if err := process.Start(ctx); err != nil {
		_, _ = process.Delete(ctx)
		return ExecResult{}, mapError(err)
	}
	result.Pid = process.Pid()
	result.StartedAt = time.Now()
	a.recordExec(newExecRecord(containerID, result, cfg, true))
	return result, nil
}

// execAttached starts an exec process and waits for it to exit.
func (a *Adapter) execAttached(ctx context.Context, containerID domain.ContainerID, execID string, process Process, cfg ExecConfig) (ExecResult, error) {
	result := ExecResult{ID: execID}
	exitCh, err := process.Wait(ctx)
	if err != nil {
		_, _ = process.Delete(ctx)
		return ExecResult{}, mapError(err)
	}
	if err := process.Start(ctx); err != nil {
		_, _ = process.Delete(ctx)
		return ExecResult{}, mapError(err)
	}
	result.Pid = process.Pid()
	result.StartedAt = time.Now()
	if err := resizeExecProcess(ctx, process, cfg); err != nil {
		return ExecResult{}, err
	}
	select {
	case <-ctx.Done():
		return ExecResult{}, a.cancelExec(ctx, containerID, process, cfg, result)
	case exit, ok := <-exitCh:
		if !ok {
			return ExecResult{}, fmt.Errorf("exec %s: exit channel closed: %w", execID, ErrServerError)
		}
		code, exitedAt, err := exit.Result()
		if err != nil {
			return ExecResult{}, mapError(err)
		}
		exitCode := int(code)
		result.ExitCode = &exitCode
		result.FinishedAt = exitedAt
	}
	return a.finishExec(ctx, containerID, process, cfg, result)
}

// resizeExecProcess resizes a terminal exec process after start, deleting the
// process when the resize fails.
func resizeExecProcess(ctx context.Context, process Process, cfg ExecConfig) error {
	if !cfg.Terminal || cfg.Width == 0 || cfg.Height == 0 {
		return nil
	}
	if err := process.Resize(ctx, cfg.Width, cfg.Height); err != nil {
		_, _ = process.Delete(ctx)
		return mapError(err)
	}
	return nil
}

// cancelExec performs the best-effort cleanup of an exec whose context ended,
// records what is known about it, and reports the context error.
func (a *Adapter) cancelExec(ctx context.Context, containerID domain.ContainerID, process Process, cfg ExecConfig, result ExecResult) error {
	cleanupCtx := context.WithoutCancel(ctx)
	_ = process.CloseIO(cleanupCtx)
	if exit, deleteErr := process.Delete(cleanupCtx); deleteErr == nil {
		if code, finishedAt, resultErr := exit.Result(); resultErr == nil {
			exitCode := int(code)
			result.ExitCode = &exitCode
			result.FinishedAt = finishedAt
		}
	}
	a.recordExec(newExecRecord(containerID, result, cfg, false))
	return ctx.Err()
}

// finishExec cleans up an exited exec process and records its result. Cleanup
// is best effort: the process already exited, and its recorded exit code is
// what callers consume.
func (a *Adapter) finishExec(ctx context.Context, containerID domain.ContainerID, process Process, cfg ExecConfig, result ExecResult) (ExecResult, error) {
	_ = process.CloseIO(ctx)
	_, _ = process.Delete(ctx)
	a.recordExec(newExecRecord(containerID, result, cfg, false))
	return result, nil
}

// ExecRecord returns the recorded state of one exec process.
func (a *Adapter) ExecRecord(execID string) (domain.ExecRecord, bool) {
	a.execMu.Lock()
	defer a.execMu.Unlock()
	record, ok := a.execs[execID]
	if !ok {
		return domain.ExecRecord{}, false
	}
	return cloneExecRecord(record), true
}

// ExecRecords returns recorded exec processes for one container ordered by
// start time.
func (a *Adapter) ExecRecords(id domain.ContainerID) []domain.ExecRecord {
	a.execMu.Lock()
	defer a.execMu.Unlock()
	records := make([]domain.ExecRecord, 0, len(a.execs))
	for _, record := range a.execs {
		if record.ContainerID == id {
			records = append(records, cloneExecRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
	return records
}

// recordExec stores an exec record for inspect handlers.
func (a *Adapter) recordExec(record domain.ExecRecord) {
	a.execMu.Lock()
	defer a.execMu.Unlock()
	a.execs[record.ID] = record
}

// newExecRecord snapshots an ExecResult as an inspect-visible record.
func newExecRecord(containerID domain.ContainerID, result ExecResult, cfg ExecConfig, running bool) domain.ExecRecord {
	return domain.ExecRecord{
		ID:          result.ID,
		ContainerID: containerID,
		Running:     running,
		ExitCode:    cloneInt(result.ExitCode),
		Command:     slices.Clone(cfg.Command),
		TTY:         cfg.Terminal,
		StartedAt:   result.StartedAt,
		FinishedAt:  result.FinishedAt,
		OpenStdin:   cfg.Stdin != nil,
		OpenStdout:  cfg.Stdout != nil,
		OpenStderr:  cfg.Stderr != nil,
		CanRemove:   !running,
		ProcessID:   int(result.Pid),
	}
}

// buildExecProcessSpec clones the container process and applies the exec
// overrides. It returns ErrInvalidArgument for unsupported users and
// ErrServerError when the stored container spec has no process.
func buildExecProcessSpec(ctx context.Context, base *specs.Process, cfg ExecConfig) (*specs.Process, error) {
	if base == nil {
		return nil, fmt.Errorf("%w: container spec has no process", ErrServerError)
	}
	process := *base
	process.Args = slices.Clone(cfg.Command)
	process.Terminal = cfg.Terminal
	opts := []oci.SpecOpts{}
	if cfg.Terminal {
		opts = append(opts, oci.WithTTY)
	}
	if env := envSlice(cfg.Env); len(env) > 0 {
		opts = append(opts, oci.WithEnv(env))
	}
	if cfg.WorkingDir != "" {
		opts = append(opts, oci.WithProcessCwd(cfg.WorkingDir))
	}
	userOpt, err := userSpecOpt(cfg.User)
	if err != nil {
		return nil, err
	}
	if userOpt != nil {
		opts = append(opts, userOpt)
	}
	spec := &oci.Spec{Process: &process}
	if err := oci.ApplyOpts(ctx, nil, &containers.Container{}, spec, opts...); err != nil {
		return nil, mapError(err)
	}
	return spec.Process, nil
}

// cloneInt copies an optional exit code so records stay immutable.
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// cloneExecRecord copies the mutable slices of an exec record.
func cloneExecRecord(record domain.ExecRecord) domain.ExecRecord {
	record.Command = slices.Clone(record.Command)
	record.ExitCode = cloneInt(record.ExitCode)
	return record
}
