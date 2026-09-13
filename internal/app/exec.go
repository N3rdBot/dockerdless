package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// pendingExec is the daemon's record of an exec created but not yet started.
type pendingExec struct {
	id         string
	container  domain.ContainerID
	command    []string
	env        map[string]string
	workingDir string
	user       string
	tty        bool
	attachIn   bool
	attachOut  bool
	attachErr  bool
	started    bool
	record     domain.ExecRecord
}

// ExecCreate implements POST /containers/{id}/exec.
func (s *Service) ExecCreate(ctx context.Context, ref string, request ports.ExecCreateRequest) (string, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	container, err := s.resolveContainer(ctx, ref)
	if err != nil {
		return "", err
	}
	if len(request.Command) == 0 {
		return "", invalidError("No exec command specified")
	}
	if state, statusErr := s.runtime.Status(ctx, container.ID); statusErr == nil && state != domain.ContainerStateRunning {
		return "", conflictError("Container %s is not running", container.ID)
	}
	id, err := newID()
	if err != nil {
		return "", serverError("failed to generate exec id: %v", err)
	}
	s.execMu.Lock()
	s.execs[id] = &pendingExec{
		id:         id,
		container:  container.ID,
		command:    append([]string(nil), request.Command...),
		env:        request.Env,
		workingDir: request.WorkingDir,
		user:       request.User,
		tty:        request.TTY,
		attachIn:   request.AttachStdin,
		attachOut:  request.AttachStdout,
		attachErr:  request.AttachStderr,
		record: domain.ExecRecord{
			ID:          id,
			ContainerID: container.ID,
			Command:     append([]string(nil), request.Command...),
			TTY:         request.TTY,
			OpenStdin:   request.AttachStdin,
			OpenStdout:  request.AttachStdout,
			OpenStderr:  request.AttachStderr,
			CanRemove:   true,
		},
	}
	s.execMu.Unlock()
	return id, nil
}

// ExecStart implements POST /exec/{id}/start. Attached executions block until
// the process exits; the returned value is its exit code.
func (s *Service) ExecStart(ctx context.Context, execID string, request ports.ExecStartRequest) (int, error) {
	pending, err := s.pendingExec(execID)
	if err != nil {
		return 0, err
	}
	stdin := request.Stdin
	stdout := request.Stdout
	stderr := request.Stderr
	if !pending.attachIn {
		stdin = nil
	}
	if !pending.attachOut {
		stdout = nil
	}
	if !pending.attachErr || pending.tty {
		stderr = nil
	}
	result, err := s.runtime.Exec(ctx, pending.container, ports.ExecRequest{
		ID:         execID,
		Command:    pending.command,
		Env:        pending.env,
		WorkingDir: pending.workingDir,
		User:       pending.user,
		TTY:        pending.tty,
		Detach:     request.Detach,
		//nolint:gosec // G115: terminal width/height come from a JSON uint and fit uint32.
		Width: uint32(request.Width),
		//nolint:gosec // G115: terminal width/height come from a JSON uint and fit uint32.
		Height: uint32(request.Height),
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		return 0, translateError(err)
	}
	s.recordExecStarted(execID, result)
	if result.ExitCode != nil {
		return *result.ExitCode, nil
	}
	return 0, nil
}

// ExecInspect implements GET /exec/{id}/json.
func (s *Service) ExecInspect(_ context.Context, execID string) (domain.ExecRecord, error) {
	if record, ok := s.runtime.ExecRecord(execID); ok {
		return record, nil
	}
	pending, err := s.pendingExec(execID)
	if err != nil {
		return domain.ExecRecord{}, err
	}
	return pending.record, nil
}

func (s *Service) pendingExec(execID string) (pendingExec, error) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	pending, ok := s.execs[execID]
	if !ok {
		return pendingExec{}, notFoundError("No such exec instance: %s", execID)
	}
	return *pending, nil
}

func (s *Service) recordExecStarted(execID string, result ports.ExecResult) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	pending, ok := s.execs[execID]
	if !ok {
		return
	}
	pending.started = true
	if record, ok := s.runtime.ExecRecord(execID); ok {
		pending.record = record
		return
	}
	pending.record.Running = result.Running
	pending.record.ProcessID = result.Pid
	pending.record.StartedAt = result.StartedAt
	pending.record.FinishedAt = result.FinishedAt
	pending.record.ExitCode = result.ExitCode
}

func newID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
