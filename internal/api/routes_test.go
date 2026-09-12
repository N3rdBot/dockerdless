package api

import (
	"net/http"
	"testing"
)

func TestMVPRouteMatrix_containsRequestedCapabilities(t *testing.T) {
	// Given
	routes := MVPRouteMatrix()
	want := []RouteCapability{
		{Method: http.MethodGet, Path: "/_ping", Supported: true},
		{Method: http.MethodHead, Path: "/_ping", Supported: true},
		{Method: http.MethodGet, Path: "/version", Supported: true},
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

	// When / Then
	if len(routes) != len(want) {
		t.Fatalf("expected %d route capabilities, got %d", len(want), len(routes))
	}
	for index, expected := range want {
		got := routes[index]
		if got.Method != expected.Method || got.Path != expected.Path || got.Supported != expected.Supported {
			t.Errorf("route %d: expected %s %s supported=%t, got %s %s supported=%t",
				index,
				expected.Method,
				expected.Path,
				expected.Supported,
				got.Method,
				got.Path,
				got.Supported,
			)
		}
		if got.Supported && got.Handler == nil {
			t.Errorf("route %d: supported route has nil handler", index)
		}
		if !got.Supported && got.Handler != nil {
			t.Errorf("route %d: unsupported route has a handler", index)
		}
	}
}

func TestMVPRouteMatrix_returnsIndependentCopies(t *testing.T) {
	// Given
	first := MVPRouteMatrix()
	second := MVPRouteMatrix()
	first[0].Path = "/changed"

	// When / Then
	if second[0].Path != "/_ping" {
		t.Fatalf("expected fresh matrix copy, got mutated path %q", second[0].Path)
	}
}
