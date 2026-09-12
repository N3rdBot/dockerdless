package containerd

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/containerd/errdefs"
)

// Sentinel errors expose runtime failures in a transport-neutral shape so the
// Docker API layer can map them to HTTP status codes without importing
// containerd error helpers.
var (
	// ErrNotFound maps to Docker HTTP 404. It is returned for missing
	// containers, tasks, images, and related resources.
	ErrNotFound = errors.New("container runtime: not found")
	// ErrConflict maps to Docker HTTP 409. It is returned for duplicate
	// names, wrong lifecycle state, and conflicting operations.
	ErrConflict = errors.New("container runtime: conflict")
	// ErrInvalidArgument maps to Docker HTTP 400. It is returned for
	// malformed requests the adapter validates itself.
	ErrInvalidArgument = errors.New("container runtime: invalid argument")
	// ErrServerError maps to Docker HTTP 500. It is returned when the runtime
	// backend is unavailable or reports an internal failure.
	ErrServerError = errors.New("container runtime: server error")
)

// mapError translates containerd errdefs into adapter sentinels. Already
// mapped errors and unknown errors are returned unchanged so callers keep
// access to context cancellation and transport details.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	// Never re-wrap an already mapped adapter error.
	switch {
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrConflict),
		errors.Is(err, ErrInvalidArgument),
		errors.Is(err, ErrServerError):
		return err
	}
	switch {
	case errdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errdefs.IsAlreadyExists(err), errdefs.IsConflict(err), errdefs.IsFailedPrecondition(err):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case errdefs.IsInvalidArgument(err), errdefs.IsOutOfRange(err):
		return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	case errdefs.IsUnavailable(err), errdefs.IsInternal(err),
		errdefs.IsUnknown(err), errdefs.IsAborted(err), errdefs.IsDataLoss(err):
		return fmt.Errorf("%w: %w", ErrServerError, err)
	default:
		return err
	}
}

// StatusCode maps an adapter error to the Docker Engine API HTTP status code.
func StatusCode(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidArgument):
		return http.StatusBadRequest
	case errors.Is(err, ErrServerError):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
