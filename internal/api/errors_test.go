package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDockerError_constructorsMapStatusAndEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		newError   func(string) *DockerError
		status     int
		kind       ErrorKind
		wantBody   string
		wantHeader string
	}{
		{
			name:       "not found",
			newError:   NewNotFound,
			status:     http.StatusNotFound,
			kind:       ErrorKindNotFound,
			wantBody:   `{"message":"missing"}`,
			wantHeader: string(jsonMediaType),
		},
		{
			name:       "conflict",
			newError:   NewConflict,
			status:     http.StatusConflict,
			kind:       ErrorKindConflict,
			wantBody:   `{"message":"missing"}`,
			wantHeader: string(jsonMediaType),
		},
		{
			name:       "invalid parameter",
			newError:   NewInvalidParameter,
			status:     http.StatusBadRequest,
			kind:       ErrorKindInvalidParameter,
			wantBody:   `{"message":"missing"}`,
			wantHeader: string(jsonMediaType),
		},
		{
			name:       "not implemented",
			newError:   NewNotImplemented,
			status:     http.StatusNotImplemented,
			kind:       ErrorKindNotImplemented,
			wantBody:   `{"message":"missing"}`,
			wantHeader: string(jsonMediaType),
		},
		{
			name:       "server error",
			newError:   NewServerError,
			status:     http.StatusInternalServerError,
			kind:       ErrorKindServer,
			wantBody:   `{"message":"missing"}`,
			wantHeader: string(jsonMediaType),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			apiError := test.newError("missing")
			recorder := httptest.NewRecorder()

			// When
			WriteDockerError(recorder, apiError)

			// Then
			if apiError.StatusCode() != test.status {
				t.Fatalf("expected status %d, got %d", test.status, apiError.StatusCode())
			}
			if apiError.Kind != test.kind {
				t.Fatalf("expected kind %q, got %q", test.kind, apiError.Kind)
			}
			if recorder.Code != test.status {
				t.Fatalf("expected response status %d, got %d", test.status, recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); got != test.wantHeader {
				t.Fatalf("expected content type %q, got %q", test.wantHeader, got)
			}
			if got := strings.TrimSpace(recorder.Body.String()); got != test.wantBody {
				t.Fatalf("expected Docker envelope %q, got %q", test.wantBody, got)
			}
		})
	}
}

func TestNewDockerError_normalizesUnknownStatusToServerError(t *testing.T) {
	// Given
	apiError := NewDockerError(http.StatusTeapot, "unexpected")

	// When
	status := apiError.StatusCode()

	// Then
	if status != http.StatusInternalServerError {
		t.Fatalf("expected unknown status to map to %d, got %d", http.StatusInternalServerError, status)
	}
	if apiError.Kind != ErrorKindServer {
		t.Fatalf("expected unknown status kind %q, got %q", ErrorKindServer, apiError.Kind)
	}
}
