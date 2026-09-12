package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/adapters/containerd"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// dockerError carries a canonical ports classification plus the Docker-facing
// message. The HTTP boundary renders DockerMessage; the cause stays available
// for logs and errors.Is chains.
type dockerError struct {
	kind    error
	message string
	cause   error
}

func (e *dockerError) Error() string { return e.message }

// DockerMessage returns the Docker API error message.
func (e *dockerError) DockerMessage() string { return e.message }

func (e *dockerError) Is(target error) bool { return target == e.kind }

func (e *dockerError) Unwrap() error { return e.cause }

func newDockerError(kind error, message string, cause error) error {
	if strings.TrimSpace(message) == "" {
		message = kind.Error()
	}
	return &dockerError{kind: kind, message: message, cause: cause}
}

// translateError maps adapter sentinels onto the canonical ports vocabulary.
// Errors that already carry a canonical classification pass through untouched.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	if isCanonicalError(err) {
		return err
	}

	switch {
	case errors.Is(err, containerd.ErrNotFound),
		errors.Is(err, buildkit.ErrNotFound),
		errors.Is(err, cni.ErrNetworkNotFound),
		errors.Is(err, domain.ErrContainerNotFound):
		return newDockerError(ports.ErrNotFound, cleanAdapterMessage(err), err)
	case errors.Is(err, containerd.ErrConflict),
		errors.Is(err, domain.ErrContainerNameConflict),
		errors.Is(err, cni.ErrNetworkExists),
		errors.Is(err, cni.ErrNetworkInUse),
		errors.Is(err, cni.ErrReservedNetworkName),
		errors.Is(err, cni.ErrPortInUse):
		return newDockerError(ports.ErrConflict, cleanAdapterMessage(err), err)
	case errors.Is(err, containerd.ErrInvalidArgument),
		errors.Is(err, domain.ErrEmptyIdentity),
		errors.Is(err, domain.ErrEmptyContainerName),
		errors.Is(err, buildkit.ErrInvalidReference),
		errors.Is(err, buildkit.ErrInvalidContext),
		errors.Is(err, buildkit.ErrInvalidAuth),
		errors.Is(err, cni.ErrInvalidNetworkName),
		errors.Is(err, cni.ErrUnsupportedNetworkDriver),
		errors.Is(err, cni.ErrUnknownNetworkMode),
		errors.Is(err, cni.ErrPortPublishWithMode),
		errors.Is(err, cni.ErrMissingNetNS),
		errors.Is(err, cni.ErrMissingContainer),
		errors.Is(err, cni.ErrIPv6Unsupported),
		errors.Is(err, cni.ErrUnallocatedPort),
		errors.Is(err, cni.ErrUnsupportedProtocol):
		return newDockerError(ports.ErrInvalidArgument, cleanAdapterMessage(err), err)
	case errors.Is(err, buildkit.ErrNoSolver):
		return newDockerError(ports.ErrNotImplemented, err.Error(), err)
	case errors.Is(err, containerd.ErrServerError),
		errors.Is(err, cni.ErrNoFreeHostPort),
		errors.Is(err, cni.ErrNilResult):
		return newDockerError(ports.ErrServerError, cleanAdapterMessage(err), err)
	case errors.Is(err, context.Canceled):
		return newDockerError(ports.ErrServerError, "request canceled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return newDockerError(ports.ErrServerError, "request timed out", err)
	default:
		return newDockerError(ports.ErrServerError, err.Error(), err)
	}
}

func isCanonicalError(err error) bool {
	switch {
	case errors.Is(err, ports.ErrNotFound),
		errors.Is(err, ports.ErrConflict),
		errors.Is(err, ports.ErrInvalidArgument),
		errors.Is(err, ports.ErrNotImplemented),
		errors.Is(err, ports.ErrServerError):
		return true
	default:
		return false
	}
}

// cleanAdapterMessage strips adapter sentinel prefixes so Docker clients see
// the backend detail without daemon-internal vocabulary.
func cleanAdapterMessage(err error) string {
	message := err.Error()
	for _, prefix := range []string{
		"container runtime: not found: ",
		"container runtime: conflict: ",
		"container runtime: invalid argument: ",
		"container runtime: server error: ",
		"image not found: ",
		"invalid image reference: ",
		"invalid build context: ",
	} {
		message = strings.TrimPrefix(message, prefix)
	}
	return message
}

func kindForStatus(status int) error {
	switch status {
	case 400:
		return ports.ErrInvalidArgument
	case 404:
		return ports.ErrNotFound
	case 409:
		return ports.ErrConflict
	case 501:
		return ports.ErrNotImplemented
	default:
		return ports.ErrServerError
	}
}

// notFoundError builds a Docker 404 with a resource-specific message.
func notFoundError(format string, args ...any) error {
	return newDockerError(ports.ErrNotFound, fmt.Sprintf(format, args...), nil)
}

// conflictError builds a Docker 409 with a resource-specific message.
func conflictError(format string, args ...any) error {
	return newDockerError(ports.ErrConflict, fmt.Sprintf(format, args...), nil)
}

// invalidError builds a Docker 400 with a resource-specific message.
func invalidError(format string, args ...any) error {
	return newDockerError(ports.ErrInvalidArgument, fmt.Sprintf(format, args...), nil)
}

// serverError builds a Docker 500 with a resource-specific message.
func serverError(format string, args ...any) error {
	return newDockerError(ports.ErrServerError, fmt.Sprintf(format, args...), nil)
}
