package api

import (
	"net/http"
)

// RouteCapability describes one Docker API endpoint and whether this MVP has
// a real handler for it.
type RouteCapability struct {
	Method    string
	Path      string
	Supported bool
	Handler   http.HandlerFunc
}

// RouteMatrix is the typed capability matrix consumed by the router.
type RouteMatrix []RouteCapability

// MVPRouteMatrix returns a fresh capability matrix for the MVP Docker API
// surface with no application service bound. Unsupported entries stay explicit
// so clients receive 501 instead of an accidental 404 or a successful no-op.
func MVPRouteMatrix() RouteMatrix {
	return RouteMatrix{
		{Method: http.MethodGet, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodHead, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodGet, Path: "/version", Supported: true, Handler: versionHandler},
		{Method: http.MethodGet, Path: "/info"},

		{Method: http.MethodGet, Path: "/images/{name}/json"},
		{Method: http.MethodPost, Path: "/images/create"},
		{Method: http.MethodGet, Path: "/images/json"},
		{Method: http.MethodPost, Path: "/build"},

		{Method: http.MethodPost, Path: "/containers/create"},
		{Method: http.MethodGet, Path: "/containers/json"},
		{Method: http.MethodGet, Path: "/containers/{id}/json"},
		{Method: http.MethodPost, Path: "/containers/{id}/start"},
		{Method: http.MethodPost, Path: "/containers/{id}/stop"},
		{Method: http.MethodGet, Path: "/containers/{id}/logs"},
		{Method: http.MethodPost, Path: "/containers/{id}/exec"},
		{Method: http.MethodDelete, Path: "/containers/{id}"},

		{Method: http.MethodGet, Path: "/networks"},
		{Method: http.MethodGet, Path: "/networks/{id}"},
		{Method: http.MethodPost, Path: "/networks/create"},
		{Method: http.MethodPost, Path: "/networks/{id}/connect"},
		{Method: http.MethodDelete, Path: "/networks/{id}"},

		{Method: http.MethodPost, Path: "/exec/{id}/start"},
		{Method: http.MethodGet, Path: "/exec/{id}/json"},
	}
}

// serviceRouteMatrix binds the real handlers for every MVP endpoint. Each
// handler calls the application service; nothing here is a success no-op.
func serviceRouteMatrix(deps Dependencies) RouteMatrix {
	handlers := newHandlers(deps)
	return RouteMatrix{
		{Method: http.MethodGet, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodHead, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodGet, Path: "/version", Supported: true, Handler: versionHandler},
		{Method: http.MethodGet, Path: "/info", Supported: true, Handler: handlers.info},

		{Method: http.MethodGet, Path: "/images/{name}/json", Supported: true, Handler: handlers.imageInspect},
		{Method: http.MethodPost, Path: "/images/create", Supported: true, Handler: handlers.imageCreate},
		{Method: http.MethodGet, Path: "/images/json", Supported: true, Handler: handlers.imageList},
		{Method: http.MethodPost, Path: "/build", Supported: true, Handler: handlers.build},

		{Method: http.MethodPost, Path: "/containers/create", Supported: true, Handler: handlers.containerCreate},
		{Method: http.MethodGet, Path: "/containers/json", Supported: true, Handler: handlers.containerList},
		{Method: http.MethodGet, Path: "/containers/{id}/json", Supported: true, Handler: handlers.containerInspect},
		{Method: http.MethodPost, Path: "/containers/{id}/start", Supported: true, Handler: handlers.containerStart},
		{Method: http.MethodPost, Path: "/containers/{id}/stop", Supported: true, Handler: handlers.containerStop},
		{Method: http.MethodGet, Path: "/containers/{id}/logs", Supported: true, Handler: handlers.containerLogs},
		{Method: http.MethodPost, Path: "/containers/{id}/exec", Supported: true, Handler: handlers.execCreate},
		{Method: http.MethodDelete, Path: "/containers/{id}", Supported: true, Handler: handlers.containerRemove},

		{Method: http.MethodGet, Path: "/networks", Supported: true, Handler: handlers.networkList},
		{Method: http.MethodGet, Path: "/networks/{id}", Supported: true, Handler: handlers.networkInspect},
		{Method: http.MethodPost, Path: "/networks/create", Supported: true, Handler: handlers.networkCreate},
		{Method: http.MethodPost, Path: "/networks/{id}/connect", Supported: true, Handler: handlers.networkConnect},
		{Method: http.MethodDelete, Path: "/networks/{id}", Supported: true, Handler: handlers.networkRemove},

		{Method: http.MethodPost, Path: "/exec/{id}/start", Supported: true, Handler: handlers.execStart},
		{Method: http.MethodGet, Path: "/exec/{id}/json", Supported: true, Handler: handlers.execInspect},
	}
}
