// Package api contains the Docker-compatible HTTP API boundary.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	return NewRouterWithRouteMatrix(MVPRouteMatrix())
}

// NewRouterWithRouteMatrix creates an instrumented router from a capability
// matrix. It is the extension point for later handler workstreams.
func NewRouterWithRouteMatrix(matrix RouteMatrix) http.Handler {
	if matrix == nil {
		matrix = MVPRouteMatrix()
	}
	router := newDockerRouter(matrix)
	versioned := NewVersionMiddleware().Wrap(router)
	return otelhttp.NewHandler(versioned, "dockerdless-api")
}

// RegisterDockerRoutes registers the currently supported unversioned routes
// on a standard library ServeMux for backwards-compatible callers.
func RegisterDockerRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}

	mux.HandleFunc("/_ping", pingHandler)
	mux.HandleFunc("/version", versionHandler)
}

type dockerRouter struct {
	routes []compiledRoute
}

type compiledRoute struct {
	capability RouteCapability
	pattern    *regexp.Regexp
}

func newDockerRouter(matrix RouteMatrix) *dockerRouter {
	routes := make([]compiledRoute, 0, len(matrix))
	for _, capability := range matrix {
		routes = append(routes, compiledRoute{
			capability: capability,
			pattern:    compileRoutePattern(capability.Path),
		})
	}
	return &dockerRouter{routes: routes}
}

func (r *dockerRouter) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	path := unversionedPath(request.URL.Path)
	for _, route := range r.routes {
		if route.capability.Method != request.Method || !route.pattern.MatchString(path) {
			continue
		}
		if !route.capability.Supported || route.capability.Handler == nil {
			WriteDockerError(w, NewNotImplemented(fmt.Sprintf(
				"endpoint %s %s is not implemented",
				request.Method,
				path,
			)))
			return
		}
		route.capability.Handler(w, request)
		return
	}

	WriteDockerError(w, NewNotFound("page not found"))
}

func compileRoutePattern(path string) *regexp.Regexp {
	pattern := regexp.QuoteMeta(path)
	pattern = strings.ReplaceAll(pattern, `\{name\}`, `.+`)
	pattern = strings.ReplaceAll(pattern, `\{id\}`, `[^/]+`)
	return regexp.MustCompile("^" + pattern + "$")
}
