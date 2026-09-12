package api

import (
	"encoding/json"
	"net/http"
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

// WriteDockerError writes a Docker-shaped JSON error response.
func WriteDockerError(w http.ResponseWriter, apiError *DockerError) {
	if apiError == nil {
		apiError = NewServerError("")
	}

	w.Header().Set("Content-Type", string(jsonMediaType))
	w.WriteHeader(apiError.StatusCode())
	if err := json.NewEncoder(w).Encode(apiError); err != nil {
		return
	}
}
