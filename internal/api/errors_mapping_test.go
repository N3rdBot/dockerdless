package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/ports"
)

func TestMapServiceError_mapsCanonicalSentinels(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{"not found", ports.ErrNotFound, http.StatusNotFound, "dockerdless: not found"},
		{"conflict", fmt.Errorf("%w: name in use", ports.ErrConflict), http.StatusConflict, "name in use"},
		{"invalid", fmt.Errorf("%w: bad request", ports.ErrInvalidArgument), http.StatusBadRequest, "bad request"},
		{"not implemented", ports.ErrNotImplemented, http.StatusNotImplemented, "dockerdless: not implemented"},
		{"server", errors.New("boom"), http.StatusInternalServerError, "boom"},
		{"deadline", context.DeadlineExceeded, http.StatusInternalServerError, "request timed out"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapServiceError(test.err)
			if mapped.StatusCode() != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, mapped.StatusCode())
			}
			if mapped.Message != test.wantMessage {
				t.Fatalf("expected message %q, got %q", test.wantMessage, mapped.Message)
			}
		})
	}
}

func TestMapServiceError_prefersDockerMessage(t *testing.T) {
	err := &dockerMessageError{message: "No such container: web"}
	mapped := mapServiceError(err)
	if mapped.Message != "No such container: web" {
		t.Fatalf("expected Docker message, got %q", mapped.Message)
	}
}

func TestMapServiceError_stripsSentinelPrefix(t *testing.T) {
	mapped := mapServiceError(fmt.Errorf("%w: disk full", ports.ErrServerError))
	if mapped.Message != "disk full" {
		t.Fatalf("expected cleaned message, got %q", mapped.Message)
	}
}

type dockerMessageError struct {
	message string
}

func (e *dockerMessageError) Error() string { return "dockerdless: not found: " + e.message }

func (e *dockerMessageError) Is(target error) bool { return target == ports.ErrNotFound }

func (e *dockerMessageError) DockerMessage() string { return e.message }

func TestDecodeJSONBody_rejectsOversizedBody(t *testing.T) {
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/containers/create", strings.NewReader(`{"Image":"`+strings.Repeat("a", 128)+`"}`))
	recorder := httptest.NewRecorder()
	var target map[string]any

	ok := decodeJSONBody(recorder, request, 32, &target)

	if ok {
		t.Fatal("expected oversized body to be rejected")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "too large") {
		t.Fatalf("expected size message, got %s", recorder.Body.String())
	}
}

func TestStreamLimiter_boundsConcurrentStreams(t *testing.T) {
	limiter := newStreamLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded acquire to time out, got %v", err)
	}
	limiter.release()
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}
