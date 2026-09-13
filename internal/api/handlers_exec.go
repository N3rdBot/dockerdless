package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/container"
	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"github.com/N3rdBot/dockerdless/internal/streams"
)

const initialResizeWait = 100 * time.Millisecond

func (h *handlers) execCreate(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/containers/", "/exec")
	var payload container.ExecCreateRequest
	if !decodeJSONBody(w, r, h.bodyLimit(), &payload) {
		return
	}
	execID, err := h.service.ExecCreate(r.Context(), id, ports.ExecCreateRequest{
		Command:      payload.Cmd,
		Env:          envMap(payload.Env),
		WorkingDir:   payload.WorkingDir,
		User:         payload.User,
		TTY:          payload.Tty,
		AttachStdin:  payload.AttachStdin,
		AttachStdout: payload.AttachStdout,
		AttachStderr: payload.AttachStderr,
	})
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, container.ExecCreateResponse{ID: execID})
}

func (h *handlers) execInspect(w http.ResponseWriter, r *http.Request) {
	id := pathParameter(r, "/exec/", "/json")
	record, err := h.service.ExecInspect(r.Context(), id)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execInspectResponse(record))
}

func (h *handlers) execStart(w http.ResponseWriter, r *http.Request) {
	execID := pathParameter(r, "/exec/", "/start")
	var payload container.ExecStartRequest
	if !decodeJSONBody(w, r, h.bodyLimit(), &payload) {
		return
	}
	if err := h.limiter.acquire(r.Context()); err != nil {
		WriteServiceError(w, err)
		return
	}
	defer h.limiter.release()

	width, height := consoleSize(payload.ConsoleSize)
	if payload.Detach {
		if _, err := h.service.ExecStart(r.Context(), execID, ports.ExecStartRequest{
			Detach: true,
			TTY:    payload.Tty,
			Width:  width,
			Height: height,
		}); err != nil {
			WriteServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	conn, readWriter, err := streams.Upgrade(w, r, !payload.Tty, apiVersionFromRequest(r))
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	defer func() { _ = conn.Close() }()

	request := ports.ExecStartRequest{
		TTY:    payload.Tty,
		Width:  width,
		Height: height,
		Stdin:  readWriter,
		Stdout: io.Writer(conn),
		Stderr: io.Writer(conn),
	}
	if payload.Tty {
		if peekedWidth, peekedHeight, ok := peekResize(conn, readWriter.Reader); ok {
			request.Width, request.Height = peekedWidth, peekedHeight
		}
		request.Stdin = newResizeFilterReader(readWriter.Reader, func(width, height uint) {
			h.logger.Debug("exec resize message received",
				zap.Uint("width", width),
				zap.Uint("height", height),
				zap.String("exec_id", execID))
		})
	} else {
		request.Stdout = streams.NewWriter(conn, streams.Stdout)
		request.Stderr = streams.NewWriter(conn, streams.Stderr)
	}

	if _, err := h.service.ExecStart(r.Context(), execID, request); err != nil {
		h.logger.Warn("exec stream ended with error",
			zap.Error(err),
			zap.String("exec_id", execID))
	}
}

func execInspectResponse(record domain.ExecRecord) container.ExecInspectResponse {
	entrypoint := ""
	var arguments []string
	if len(record.Command) > 0 {
		entrypoint = record.Command[0]
		arguments = append([]string(nil), record.Command[1:]...)
	}
	return container.ExecInspectResponse{
		ID:       record.ID,
		Running:  record.Running,
		ExitCode: record.ExitCode,
		ProcessConfig: &container.ExecProcessConfig{
			Tty:        record.TTY,
			Entrypoint: entrypoint,
			Arguments:  arguments,
		},
		OpenStdin:   record.OpenStdin,
		OpenStdout:  record.OpenStdout,
		OpenStderr:  record.OpenStderr,
		CanRemove:   record.CanRemove,
		ContainerID: string(record.ContainerID),
		Pid:         record.ProcessID,
	}
}

func consoleSize(size *[2]uint) (uint, uint) {
	if size == nil {
		return 0, 0
	}
	return size[1], size[0]
}

// peekResize waits briefly for the first TTY resize message so the initial
// terminal size reaches the exec process at start. Buffered stdin survives the
// peek because Peek never consumes without a successful decode.
func peekResize(conn net.Conn, reader *bufio.Reader) (uint, uint, bool) {
	if err := conn.SetReadDeadline(time.Now().Add(initialResizeWait)); err != nil {
		return 0, 0, false
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	for {
		available := reader.Buffered()
		if available > 0 {
			peeked, err := reader.Peek(available)
			if err != nil {
				return 0, 0, false
			}
			if width, height, consumed, ok := decodeResize(peeked); ok {
				_, _ = reader.Discard(consumed)
				return width, height, true
			} else if !completeJSONPrefix(peeked) {
				return 0, 0, false
			}
		}
		if _, err := reader.Peek(available + 1); err != nil {
			return 0, 0, false
		}
	}
}

// resizeFilterReader consumes resize messages that sit at the head of the
// stream and passes every other byte through as stdin.
type resizeFilterReader struct {
	source   *bufio.Reader
	onResize func(uint, uint)
}

func newResizeFilterReader(source *bufio.Reader, onResize func(uint, uint)) *resizeFilterReader {
	return &resizeFilterReader{source: source, onResize: onResize}
}

func (r *resizeFilterReader) Read(p []byte) (int, error) {
	for {
		available := r.source.Buffered()
		if available == 0 {
			return r.source.Read(p)
		}
		peeked, err := r.source.Peek(available)
		if err != nil {
			return r.source.Read(p)
		}
		width, height, consumed, ok := decodeResize(peeked)
		if !ok {
			return r.source.Read(p)
		}
		_, _ = r.source.Discard(consumed)
		if r.onResize != nil {
			r.onResize(width, height)
		}
	}
}

// decodeResize parses one JSON resize message from the head of buf and reports
// how many bytes it consumed.
func decodeResize(buf []byte) (width, height uint, consumed int, ok bool) {
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0, 0, 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var message streams.ResizeMessage
	if err := decoder.Decode(&message); err != nil {
		return 0, 0, 0, false
	}
	if message.Width == 0 && message.Height == 0 {
		return 0, 0, 0, false
	}
	offset := int(decoder.InputOffset())
	leading := len(buf) - len(trimmed)
	return message.Width, message.Height, leading + offset, true
}

// completeJSONPrefix reports whether more bytes could still complete a JSON
// object, so the peek keeps waiting for a split resize message.
func completeJSONPrefix(buf []byte) bool {
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var raw json.RawMessage
	err := json.Unmarshal(trimmed, &raw)
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}
