package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"

	"github.com/N3rdBot/dockerdless/internal/ports"
)

// TestImageInspectResponse_alwaysCarriesConfigWithExposedPorts guards the
// contract testcontainers-go v0.44 relies on: ImageInspect.Config must never
// be nil, and the typed ExposedPorts set must always be non-nil so a client can
// range over it even for images that expose nothing.
func TestImageInspectResponse_alwaysCarriesConfigWithExposedPorts(t *testing.T) {
	// Given an image with no configuration detail at all.
	detail := ports.ImageDetail{ID: "sha256:abc", RepoTags: []string{"alpine:3.20"}}

	// When
	response := imageInspectResponse(detail)

	// Then
	if response.Config == nil {
		t.Fatal("image inspect Config must never be nil: testcontainers-go dereferences ImageInspect.Config.ExposedPorts")
	}
	if response.Config.ExposedPorts == nil {
		t.Fatal("image inspect Config.ExposedPorts must be non-nil so clients can range over it")
	}
}

// TestImageInspect_mapsImageConfigFields proves the adapter's OCI image config
// reaches the Docker-facing InspectResponse.Config envelope field by field.
func TestImageInspect_mapsImageConfigFields(t *testing.T) {
	// Given
	detail := ports.ImageDetail{
		ID:       "sha256:abc",
		RepoTags: []string{"dls-compat:latest"},
		Config: &ports.ImageConfig{
			User:         "1000:1000",
			ExposedPorts: []string{"8080/tcp", "53/udp"},
			Env:          []string{"DLS_COMPAT_ENV=present"},
			Entrypoint:   []string{"/dls-helper"},
			Cmd:          []string{"serve", "8080"},
			Volumes:      []string{"/data"},
			WorkingDir:   "/work",
			Labels:       map[string]string{"dls": "compat"},
			StopSignal:   "SIGTERM",
		},
	}

	// When
	response := imageInspectResponse(detail)

	// Then
	config := response.Config
	if config == nil {
		t.Fatal("Config is nil")
	}
	if config.User != "1000:1000" {
		t.Fatalf("Config.User = %q, want 1000:1000", config.User)
	}
	if len(config.ExposedPorts) != 2 {
		t.Fatalf("Config.ExposedPorts = %v, want 8080/tcp and 53/udp", config.ExposedPorts)
	}
	if _, ok := config.ExposedPorts["8080/tcp"]; !ok {
		t.Fatalf("Config.ExposedPorts = %v, want 8080/tcp", config.ExposedPorts)
	}
	if len(config.Env) != 1 || config.Env[0] != "DLS_COMPAT_ENV=present" {
		t.Fatalf("Config.Env = %v, want [DLS_COMPAT_ENV=present]", config.Env)
	}
	if len(config.Entrypoint) != 1 || config.Entrypoint[0] != "/dls-helper" {
		t.Fatalf("Config.Entrypoint = %v, want [/dls-helper]", config.Entrypoint)
	}
	if len(config.Cmd) != 2 || config.Cmd[0] != "serve" || config.Cmd[1] != "8080" {
		t.Fatalf("Config.Cmd = %v, want [serve 8080]", config.Cmd)
	}
	if _, ok := config.Volumes["/data"]; !ok {
		t.Fatalf("Config.Volumes = %v, want /data", config.Volumes)
	}
	if config.WorkingDir != "/work" {
		t.Fatalf("Config.WorkingDir = %q, want /work", config.WorkingDir)
	}
	if config.Labels["dls"] != "compat" {
		t.Fatalf("Config.Labels = %v, want dls=compat", config.Labels)
	}
	if config.StopSignal != "SIGTERM" {
		t.Fatalf("Config.StopSignal = %q, want SIGTERM", config.StopSignal)
	}
}

// TestHandlers_imageInspectHTTP_emitsNonNullConfig decodes the real HTTP body
// so the wire shape (not just the Go struct) is covered.
func TestHandlers_imageInspectHTTP_emitsNonNullConfig(t *testing.T) {
	service := &fakeService{imageInspect: func(context.Context, string) (ports.ImageDetail, error) {
		return ports.ImageDetail{
			ID:       "sha256:abc",
			RepoTags: []string{"dls-compat:latest"},
			Config: &ports.ImageConfig{
				ExposedPorts: []string{"8080/tcp"},
				Env:          []string{"A=b"},
			},
		}, nil
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1.44/images/dls-compat:latest/json", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response image.InspectResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	if response.Config == nil {
		t.Fatal("decoded Config is nil")
	}
	if _, ok := response.Config.ExposedPorts["8080/tcp"]; !ok {
		t.Fatalf("decoded Config.ExposedPorts = %v, want 8080/tcp", response.Config.ExposedPorts)
	}
	if len(response.Config.Env) != 1 || response.Config.Env[0] != "A=b" {
		t.Fatalf("decoded Config.Env = %v, want [A=b]", response.Config.Env)
	}
}

// TestHandlers_imageRemove_returnsDeleteEnvelopeAndForwardsOptions covers the
// Docker success shape: HTTP 200 with a JSON array of DeleteResponse items, as
// the Moby client unconditionally JSON-decodes the body.
func TestHandlers_imageRemove_returnsDeleteEnvelopeAndForwardsOptions(t *testing.T) {
	var gotRef string
	var gotForce bool
	service := &fakeService{imageRemove: func(_ context.Context, ref string, force bool) (ports.ImageRemoveResult, error) {
		gotRef, gotForce = ref, force
		return ports.ImageRemoveResult{ID: "sha256:abc", Untagged: "alpine:3.20"}, nil
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/v1.44/images/alpine:3.20?force=1&noprune=1", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (Docker answers DELETE /images/{name} with 200 and a JSON body)", recorder.Code, recorder.Body.String())
	}
	if gotRef != "alpine:3.20" {
		t.Fatalf("service ref = %q, want alpine:3.20", gotRef)
	}
	if !gotForce {
		t.Fatal("service force = false, want true")
	}
	var items []image.DeleteResponse
	if err := json.NewDecoder(recorder.Body).Decode(&items); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if len(items) != 1 || items[0].Untagged != "alpine:3.20" {
		t.Fatalf("delete items = %+v, want one Untagged alpine:3.20 entry", items)
	}
}

// TestHandlers_imageRemove_missingImageIsDocker404 asserts the Docker-shaped
// not-found envelope for an absent image.
func TestHandlers_imageRemove_missingImageIsDocker404(t *testing.T) {
	service := &fakeService{imageRemove: func(context.Context, string, bool) (ports.ImageRemoveResult, error) {
		return ports.ImageRemoveResult{}, fmt.Errorf("%w: No such image: gone:latest", ports.ErrNotFound)
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/v1.44/images/gone:latest", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "No such image: gone:latest") {
		t.Fatalf("body = %s, want Docker No such image message", recorder.Body.String())
	}
}

// TestHandlers_imageRemove_inUseImageIsDocker409 asserts the in-use conflict is
// surfaced as Docker's 409 conflict envelope.
func TestHandlers_imageRemove_inUseImageIsDocker409(t *testing.T) {
	service := &fakeService{imageRemove: func(context.Context, string, bool) (ports.ImageRemoveResult, error) {
		return ports.ImageRemoveResult{}, fmt.Errorf(
			"%w: conflict: unable to delete abcdef123456 (must be forced) - image is being used by stopped container 0123456789ab",
			ports.ErrConflict)
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/v1.44/images/alpine:3.20", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "must be forced") {
		t.Fatalf("body = %s, want Docker conflict message", recorder.Body.String())
	}
}
