package api

import (
	"encoding/json"
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

// MVPRouteMatrix returns a fresh capability matrix for the supported Docker
// API surface. Unsupported entries remain explicit so clients receive 501
// instead of an accidental 404 or a successful no-op.
func MVPRouteMatrix() RouteMatrix {
	return RouteMatrix{
		{Method: http.MethodGet, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodHead, Path: "/_ping", Supported: true, Handler: pingHandler},
		{Method: http.MethodGet, Path: "/version", Supported: true, Handler: versionHandler},
		{Method: http.MethodGet, Path: "/info"},

		{Method: http.MethodGet, Path: "/images/{name}/json"},
		{Method: http.MethodPost, Path: "/images/create"},
		{Method: http.MethodGet, Path: "/images/json"},

		{Method: http.MethodPost, Path: "/containers/create"},
		{Method: http.MethodGet, Path: "/containers/json"},
		{Method: http.MethodGet, Path: "/containers/{id}/json"},
		{Method: http.MethodPost, Path: "/containers/{id}/start"},
		{Method: http.MethodPost, Path: "/containers/{id}/stop"},
		{Method: http.MethodGet, Path: "/containers/{id}/logs"},
		{Method: http.MethodPost, Path: "/containers/{id}/exec"},
		{Method: http.MethodDelete, Path: "/containers/{id}"},

		{Method: http.MethodGet, Path: "/networks"},
		{Method: http.MethodPost, Path: "/networks/create"},
		{Method: http.MethodPost, Path: "/networks/{id}/connect"},
		{Method: http.MethodDelete, Path: "/networks/{id}"},

		{Method: http.MethodPost, Path: "/exec/{id}/start"},
		{Method: http.MethodGet, Path: "/exec/{id}/json"},
	}
}

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := w.Write([]byte("OK")); err != nil {
		return
	}
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	response := struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
	}{
		Version:    "0.0.0-dev",
		APIVersion: AdvertisedAPIVersion,
	}

	body, err := json.Marshal(response)
	if err != nil {
		WriteDockerError(w, NewServerError(err.Error()))
		return
	}
	w.Header().Set("Content-Type", string(jsonMediaType))
	if _, err := w.Write(append(body, '\n')); err != nil {
		return
	}
}
