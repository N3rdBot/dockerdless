package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestRouter_baselinePingReturnsOK(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_ping", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected _ping status %d, got %d", http.StatusOK, recorder.Code)
	}
	if got := recorder.Body.String(); got != "OK" {
		t.Fatalf("expected _ping body %q, got %q", "OK", got)
	}
}

func TestRouter_baselineVersionReturnsJSON(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/version", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected /version status %d, got %d", http.StatusOK, recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != jsonMediaType {
		t.Fatalf("expected /version content type %q, got %q", jsonMediaType, got)
	}
	var response struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode /version response: %v", err)
	}
	if response.Version != "0.0.0-dev" {
		t.Fatalf("expected daemon version %q, got %q", "0.0.0-dev", response.Version)
	}
	if response.APIVersion == "" {
		t.Fatal("expected /version to include ApiVersion")
	}
}

func TestRouter_rejectsVersionBelowMinimumWithDockerError(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1.0/info", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	assertVersionUnsupportedResponse(t, recorder)
}

func TestRouter_rejectsVersionAboveMaximumWithDockerError(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v9.99/info", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	assertVersionUnsupportedResponse(t, recorder)
}

func assertVersionUnsupportedResponse(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected version rejection status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != jsonMediaType {
		t.Fatalf("expected version rejection content type %q, got %q", jsonMediaType, got)
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode version rejection response: %v", err)
	}
	if response.Message == "" {
		t.Fatal("expected version rejection message")
	}
}

func TestRouter_setsCompatibilityHeadersOnEveryResponse(t *testing.T) {
	// Given
	handler := NewRouter()
	paths := []string{"/_ping", "/version", "/v1.44/info", "/unknown"}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// When
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			// Then
			if got := recorder.Header().Get("Api-Version"); got != AdvertisedAPIVersion {
				t.Fatalf("expected Api-Version %q, got %q", AdvertisedAPIVersion, got)
			}
			if got := recorder.Header().Get("Builder-Version"); got != AdvertisedBuilderVersion {
				t.Fatalf("expected Builder-Version %q, got %q", AdvertisedBuilderVersion, got)
			}
			if got := recorder.Header().Get("Ostype"); got != runtime.GOOS {
				t.Fatalf("expected Ostype %q, got %q", runtime.GOOS, got)
			}
		})
	}
}

func TestRouter_returnsNotImplementedForRecognizedUnsupportedRoute(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1.44/images/json", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, recorder.Code)
	}
	assertDockerEnvelope(t, recorder, "endpoint GET /images/json is not implemented")
}

func TestRouter_returnsNotFoundForUnknownRoute(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1.44/unknown", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	assertDockerEnvelope(t, recorder, "page not found")
}

func TestRouter_matchesVersionedParameterizedRoute(t *testing.T) {
	// Given
	handler := NewRouter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1.44/images/library/alpine/json", nil)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, req)

	// Then
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("expected image route to be recognized with status %d, got %d", http.StatusNotImplemented, recorder.Code)
	}
	assertDockerEnvelope(t, recorder, "endpoint GET /images/library/alpine/json is not implemented")
}

func TestRouter_manualHTTPRoundTrip(t *testing.T) {
	// Given
	server := httptest.NewServer(NewRouter())
	defer server.Close()

	requests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/_ping"},
		{method: http.MethodHead, path: "/_ping"},
		{method: http.MethodGet, path: "/version"},
		{method: http.MethodGet, path: "/v1.0/info"},
		{method: http.MethodGet, path: "/v1.44/info"},
		{method: http.MethodGet, path: "/unknown"},
		{method: http.MethodGet, path: "/images/json"},
	}

	for _, test := range requests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			// When
			response, err := manualRequest(t.Context(), server.URL+test.path, test.method)
			if err != nil {
				t.Fatalf("manual HTTP request: %v", err)
			}

			// Then
			t.Logf("%s %s -> status=%d Api-Version=%q Builder-Version=%q Ostype=%q body=%q",
				test.method,
				test.path,
				response.status,
				response.apiVersion,
				response.builderVersion,
				response.ostype,
				response.body,
			)
		})
	}
}

type manualResponse struct {
	status         int
	apiVersion     string
	builderVersion string
	ostype         string
	body           string
}

func manualRequest(ctx context.Context, url, method string) (manualResponse, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return manualResponse{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return manualResponse{}, err
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return manualResponse{}, err
	}
	if err := response.Body.Close(); err != nil {
		return manualResponse{}, err
	}
	return manualResponse{
		status:         response.StatusCode,
		apiVersion:     response.Header.Get("Api-Version"),
		builderVersion: response.Header.Get("Builder-Version"),
		ostype:         response.Header.Get("Ostype"),
		body:           string(body),
	}, nil
}

func assertDockerEnvelope(t *testing.T, recorder *httptest.ResponseRecorder, message string) {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); got != jsonMediaType {
		t.Fatalf("expected Docker content type %q, got %q", jsonMediaType, got)
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode Docker error: %v", err)
	}
	if response.Message != message {
		t.Fatalf("expected Docker message %q, got %q", message, response.Message)
	}
}
