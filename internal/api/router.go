// Package api contains the Docker-compatible HTTP API boundary.
package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/observability"
	dockertypes "github.com/moby/moby/api/types"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

const jsonMediaType dockertypes.MediaType = "application/json"

// NewRouter creates an instrumented Docker API router with no application
// service wired: recognized data endpoints answer 501.
func NewRouter() http.Handler {
	return NewRouterWithRouteMatrix(MVPRouteMatrix())
}

// NewRouterWithDependencies creates an instrumented Docker API router wired to
// the supplied application service. A nil service keeps every data endpoint
// recognized but unimplemented (501) rather than registering a success no-op.
func NewRouterWithDependencies(deps Dependencies) http.Handler {
	if deps.Service == nil {
		return NewRouterWithRouteMatrix(MVPRouteMatrix())
	}
	return NewRouterWithRouteMatrix(serviceRouteMatrix(deps))
}

// NewHandler builds the fully instrumented handler served on the daemon
// socket: request-id/trace correlation and access logging are outermost, then
// OpenTelemetry HTTP metrics/spans, then Docker version negotiation, then the
// route matrix.
func NewHandler(deps Dependencies, logger *zap.Logger) http.Handler {
	router := NewRouterWithDependencies(deps)
	versioned := NewVersionMiddleware().Wrap(router)
	instrumented := otelhttp.NewHandler(versioned, "dockerdless-api")
	return observability.Middleware(logger)(instrumented)
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
