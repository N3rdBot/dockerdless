package containerd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/containerd/errdefs"
)

func TestMapErrorMapsContainerdErrdefs(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
	}{
		{name: "not found", err: errdefs.ErrNotFound, target: ErrNotFound},
		{name: "already exists", err: errdefs.ErrAlreadyExists, target: ErrConflict},
		{name: "conflict", err: errdefs.ErrConflict, target: ErrConflict},
		{name: "failed precondition", err: errdefs.ErrFailedPrecondition, target: ErrConflict},
		{name: "invalid argument", err: errdefs.ErrInvalidArgument, target: ErrInvalidArgument},
		{name: "out of range", err: errdefs.ErrOutOfRange, target: ErrInvalidArgument},
		{name: "unavailable", err: errdefs.ErrUnavailable, target: ErrServerError},
		{name: "internal", err: errdefs.ErrInternal, target: ErrServerError},
		{name: "wrapped not found", err: fmt.Errorf("container abc: %w", errdefs.ErrNotFound), target: ErrNotFound},
		{name: "wrapped failed precondition", err: fmt.Errorf("task running: %w", errdefs.ErrFailedPrecondition), target: ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapError(test.err)
			if !errors.Is(mapped, test.target) {
				t.Fatalf("mapError(%v) = %v, want errors.Is(..., %v)", test.err, mapped, test.target)
			}
			if !errors.Is(mapped, test.err) {
				t.Fatalf("mapError(%v) = %v, lost the original error", test.err, mapped)
			}
		})
	}
}

func TestMapErrorPreservesUnmappedErrors(t *testing.T) {
	if got := mapError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("mapError(context.Canceled) = %v, want context.Canceled", got)
	}
	sentinel := errors.New("transport detail")
	if got := mapError(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("mapError(unknown) = %v, want the original error", got)
	}
	mapped := mapError(errdefs.ErrNotFound)
	if got := mapError(mapped); !errors.Is(got, mapped) {
		t.Fatalf("mapError must not re-wrap mapped errors: got %v, want %v", got, mapped)
	}
}

func TestStatusCodeMapsSentinels(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: 200},
		{name: "not found", err: ErrNotFound, want: 404},
		{name: "wrapped not found", err: fmt.Errorf("container: %w", ErrNotFound), want: 404},
		{name: "conflict", err: ErrConflict, want: 409},
		{name: "invalid argument", err: ErrInvalidArgument, want: 400},
		{name: "server error", err: ErrServerError, want: 500},
		{name: "unknown", err: errors.New("boom"), want: 500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StatusCode(test.err); got != test.want {
				t.Fatalf("StatusCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}
