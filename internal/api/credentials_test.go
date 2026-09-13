package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/N3rdBot/dockerdless/internal/ports"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const (
	leakTestPassword = "hunter2-not-a-real-secret"
	leakTestToken    = "s3cr3t-refresh-token"
)

// TestRegistryAuthNeverAppearsInResponsesOrLogs proves the HTTP boundary does
// not echo the X-Registry-Auth header: a request carrying credentials produces
// a response body and access-log records that never contain the secret, while
// the decoded credentials still reach the service intact.
func TestRegistryAuthNeverAppearsInResponsesOrLogs(t *testing.T) {
	fake := &authCaptureService{}
	core, observed := observer.New(zap.DebugLevel)
	handler := NewHandler(Dependencies{Service: fake}, zap.New(core))

	for _, path := range []string{
		"/images/create?fromImage=alpine:3.20",
		"/build?t=leak-test",
	} {
		request, err := http.NewRequest(http.MethodPost, path, http.NoBody)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		request.Header.Set("X-Registry-Auth", authHeaderValue(t, "user", leakTestPassword))
		recorder := newResponseRecorder()
		handler.ServeHTTP(recorder, request)

		body := recorder.body.Bytes()
		if bytes.Contains(body, []byte(leakTestPassword)) {
			t.Fatalf("%s response leaked password: %s", path, body)
		}
		if bytes.Contains(body, []byte(leakTestToken)) {
			t.Fatalf("%s response leaked token: %s", path, body)
		}
	}

	if fake.pullAuth == nil || fake.pullAuth.Password != leakTestPassword {
		t.Fatal("decoded credentials did not reach the service")
	}

	for _, entry := range observed.All() {
		for _, value := range entry.ContextMap() {
			if rendered, ok := value.(string); ok && bytes.Contains([]byte(rendered), []byte(leakTestPassword)) {
				t.Fatalf("log record %q leaked the password: %v", entry.Message, entry.ContextMap())
			}
		}
	}
}

// TestRegistryAuthCannotReachZapRecords proves that logging the transport
// credentials through zap only records presence flags, never values.
func TestRegistryAuthCannotReachZapRecords(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	auth := &ports.RegistryAuth{
		Username:      "dockerdless-user",
		Password:      leakTestPassword,
		IdentityToken: leakTestToken,
		ServerAddress: "registry.example.test",
	}
	logger.Info("pull request", zap.Any("auth", auth))

	records := observed.FilterMessage("pull request").All()
	if len(records) != 1 {
		t.Fatalf("expected one audit record for the pull request, got %d", len(records))
	}
	rendered := records[0].ContextMap()
	if rendered["auth_password"] == leakTestPassword {
		t.Fatal("zap record exposed the password value")
	}
	if rendered["auth_identity_token"] == leakTestToken {
		t.Fatal("zap record exposed the identity token value")
	}
	if rendered["auth_username"] == "dockerdless-user" {
		t.Fatal("zap record exposed the username value")
	}
}

func authHeaderValue(t *testing.T, username, password string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatalf("marshal auth payload: %v", err)
	}
	return base64.URLEncoding.EncodeToString(payload)
}

// authCaptureService records the decoded credentials that reach the service
// and succeeds, so the assertions focus on boundary rendering, not error paths.
type authCaptureService struct {
	Service
	pullAuth *ports.RegistryAuth
}

func (s *authCaptureService) ImagePull(_ context.Context, request ports.PullRequest, _ io.Writer) error {
	s.pullAuth = request.Auth
	return nil
}

func (s *authCaptureService) ImageBuild(context.Context, ports.BuildRequest, io.Writer) error {
	return nil
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: http.Header{}, code: http.StatusOK}
}

type responseRecorder struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) WriteHeader(code int)        { r.code = code }
func (r *responseRecorder) Write(p []byte) (int, error) { return r.body.Write(p) }
