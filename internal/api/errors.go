package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/ports"
)

// ErrorKind identifies the Docker-compatible class of an API error.
type ErrorKind string

const (
	// ErrorKindNotFound is returned when an endpoint or resource is absent.
	ErrorKindNotFound ErrorKind = "NotFound"
	// ErrorKindConflict is returned when a request conflicts with current state.
	ErrorKindConflict ErrorKind = "Conflict"
	// ErrorKindInvalidParameter is returned for invalid request parameters.
	ErrorKindInvalidParameter ErrorKind = "InvalidParameter"
	// ErrorKindNotImplemented is returned for recognized, unsupported endpoints.
	ErrorKindNotImplemented ErrorKind = "NotImplemented"
	// ErrorKindUnavailable is returned when a bounded resource is exhausted.
	ErrorKindUnavailable ErrorKind = "Unavailable"
	// ErrorKindServer is returned for unexpected server failures.
	ErrorKindServer ErrorKind = "ServerError"
)

// DockerError is the Docker Engine API error envelope.
//
// Status and Kind are intentionally omitted from JSON. Docker clients expect
// every error body to contain exactly one message property.
type DockerError struct {
	Status  int       `json:"-"`
	Kind    ErrorKind `json:"-"`
	Message string    `json:"message"`
}

// Error returns the message carried by the Docker error envelope.
func (e *DockerError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// StatusCode returns the HTTP status associated with the error.
func (e *DockerError) StatusCode() int {
	if e == nil || e.Status < http.StatusBadRequest {
		return http.StatusInternalServerError
	}
	return e.Status
}

// NewDockerError maps a supported HTTP status to a Docker error kind.
// Unknown statuses are normalized to a server error so callers cannot emit an
// unclassified error response.
func NewDockerError(status int, message string) *DockerError {
	switch status {
	case http.StatusNotFound:
		return NewNotFound(message)
	case http.StatusConflict:
		return NewConflict(message)
	case http.StatusBadRequest:
		return NewInvalidParameter(message)
	case http.StatusNotImplemented:
		return NewNotImplemented(message)
	case http.StatusInternalServerError:
		return NewServerError(message)
	default:
		return NewServerError(message)
	}
}

// NewNotFound creates a 404 Docker error.
func NewNotFound(message string) *DockerError {
	return newDockerError(http.StatusNotFound, ErrorKindNotFound, message, "page not found")
}

// NewConflict creates a 409 Docker error.
func NewConflict(message string) *DockerError {
	return newDockerError(http.StatusConflict, ErrorKindConflict, message, "request conflicts with current state")
}

// NewInvalidParameter creates a 400 Docker error.
func NewInvalidParameter(message string) *DockerError {
	return newDockerError(http.StatusBadRequest, ErrorKindInvalidParameter, message, "invalid parameter")
}

// NewNotImplemented creates a 501 Docker error.
func NewNotImplemented(message string) *DockerError {
	return newDockerError(http.StatusNotImplemented, ErrorKindNotImplemented, message, "operation not implemented")
}

// NewUnavailable creates a 503 Docker error for exhausted bounded resources.
func NewUnavailable(message string) *DockerError {
	return newDockerError(http.StatusServiceUnavailable, ErrorKindUnavailable, message, "service unavailable")
}

// NewServerError creates a 500 Docker error.
func NewServerError(message string) *DockerError {
	return newDockerError(http.StatusInternalServerError, ErrorKindServer, message, "internal server error")
}

func newDockerError(status int, kind ErrorKind, message, fallback string) *DockerError {
	if message == "" {
		message = fallback
	}
	return &DockerError{Status: status, Kind: kind, Message: message}
}

// dockerMessenger lets an application error carry a Docker-facing message
// distinct from its wrapped chain.
type dockerMessenger interface {
	DockerMessage() string
}

// WriteServiceError maps an application error onto the Docker error envelope.
func WriteServiceError(w http.ResponseWriter, err error) {
	WriteDockerError(w, mapServiceError(err))
}

// mapServiceError translates canonical ports classifications into Docker
// status codes and messages.
func mapServiceError(err error) *DockerError {
	if err == nil {
		err = errors.New("")
	}
	message := err.Error()
	var messenger dockerMessenger
	if errors.As(err, &messenger) {
		message = messenger.DockerMessage()
	}
	message = cleanServiceMessage(message)

	switch {
	case errors.Is(err, ports.ErrNotFound):
		return NewNotFound(message)
	case errors.Is(err, ports.ErrConflict):
		return NewConflict(message)
	case errors.Is(err, ports.ErrInvalidArgument):
		return NewInvalidParameter(message)
	case errors.Is(err, ports.ErrNotImplemented):
		return NewNotImplemented(message)
	case errors.Is(err, context.DeadlineExceeded):
		return NewServerError("request timed out")
	case errors.Is(err, context.Canceled):
		return NewServerError("request canceled")
	default:
		return NewServerError(message)
	}
}

func cleanServiceMessage(message string) string {
	for _, prefix := range []string{
		"dockerdless: not found: ",
		"dockerdless: conflict: ",
		"dockerdless: invalid argument: ",
		"dockerdless: not implemented: ",
		"dockerdless: server failure: ",
	} {
		message = strings.TrimPrefix(message, prefix)
	}
	return message
}

// serviceMessage renders the Docker-facing message for a service error.
func serviceMessage(err error) string {
	if err == nil {
		return ""
	}
	var messenger dockerMessenger
	if errors.As(err, &messenger) {
		return cleanServiceMessage(messenger.DockerMessage())
	}
	return cleanServiceMessage(err.Error())
}

// decodeJSONBody decodes a bounded JSON request body into target. On failure
// it writes the Docker error response and reports false.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	body := r.Body
	if limit > 0 {
		body = http.MaxBytesReader(w, r.Body, limit)
	}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			WriteDockerError(w, NewInvalidParameter("request body is too large"))
			return false
		}
		if errors.Is(err, io.EOF) {
			WriteDockerError(w, NewInvalidParameter("request body must not be empty"))
			return false
		}
		WriteDockerError(w, NewInvalidParameter("invalid JSON body: "+err.Error()))
		return false
	}
	return true
}

// streamLimiter bounds concurrent long-lived streams such as log follow and
// hijacked exec sessions.
type streamLimiter struct {
	slots chan struct{}
}

func newStreamLimiter(limit int) *streamLimiter {
	if limit <= 0 {
		limit = DefaultStreamLimit
	}
	return &streamLimiter{slots: make(chan struct{}, limit)}
}

func (l *streamLimiter) acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *streamLimiter) release() {
	if l == nil {
		return
	}
	select {
	case <-l.slots:
	default:
	}
}

// WriteDockerError writes a Docker-shaped JSON error response.
func WriteDockerError(w http.ResponseWriter, apiError *DockerError) {
	if apiError == nil {
		apiError = NewServerError("")
	}

	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(apiError.StatusCode())
	if err := json.NewEncoder(w).Encode(apiError); err != nil {
		return
	}
}
