// Package api contains the Docker-compatible HTTP API boundary.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	dockertypes "github.com/moby/moby/api/types"
	dockerclient "github.com/moby/moby/client"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const jsonMediaType dockertypes.MediaType = "application/json"

// Dependencies contains optional clients used by future Docker API handlers.
// Keeping these dependencies at the HTTP boundary prevents adapters from
// leaking into the domain model.
type Dependencies struct {
	DockerClient *dockerclient.Client
}

// Server is the API server composition boundary. Socket binding and serving
// are intentionally deferred until the daemon lifecycle is implemented.
type Server struct {
	socketPath string
	handler    http.Handler
}

// NewServer validates the API boundary and stores the configured handler.
func NewServer(socketPath string, handler http.Handler) (*Server, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("API socket path must not be empty")
	}
	if handler == nil {
		return nil, errors.New("API handler must not be nil")
	}

	return &Server{socketPath: socketPath, handler: handler}, nil
}

// Handler returns the HTTP handler registered with the server boundary.
func (s *Server) Handler() http.Handler {
	if s == nil {
		return nil
	}
	return s.handler
}

// SocketPath returns the configured Unix socket path.
func (s *Server) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
}

// NewRouter creates an instrumented Docker API router with no external
// clients configured.
func NewRouter() http.Handler {
	return NewRouterWithDependencies(Dependencies{})
}

// NewRouterWithDependencies creates an instrumented Docker API router.
func NewRouterWithDependencies(_ Dependencies) http.Handler {
	mux := http.NewServeMux()
	RegisterDockerRoutes(mux)
	return otelhttp.NewHandler(mux, "dockerdless-api")
}

// RegisterDockerRoutes registers the initial Docker API route placeholders.
func RegisterDockerRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}

	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			return
		}
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", string(jsonMediaType))
		response := struct {
			Version    string `json:"Version"`
			APIVersion string `json:"ApiVersion"`
		}{
			Version:    "0.0.0-dev",
			APIVersion: "1.0",
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, "failed to encode version response", http.StatusInternalServerError)
		}
	})
}
