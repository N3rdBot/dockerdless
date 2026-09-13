package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}
}

func TestServer_bindsSocketWith0660AndServesRequests(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	server, err := NewServer(socketPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = server.http.Serve(listener) }()

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("expected socket permissions 0660, got %04o", perm)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix/_ping", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := unixHTTPClient(socketPath).Do(request)
	if err != nil {
		t.Fatalf("request over unix socket: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Fatalf("expected 200 OK, got %d %q", response.StatusCode, body)
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected socket removed after shutdown, stat err=%v", err)
	}
}

func TestServer_removesStaleSocketFile(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	stale, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("listen stale socket: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected stale socket file to remain: %v", err)
	}

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err != nil {
		t.Fatalf("expected stale socket to be replaced: %v", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestServer_refusesLiveSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	live, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("listen live socket: %v", err)
	}
	defer func() { _ = live.Close() }()

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected live-socket refusal, got %v", err)
	}
}

func TestServer_runShutsDownGracefullyOnContextCancel(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	server, err := NewServer(socketPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	waitForSocket(t, socketPath)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix/_ping", nil)
	if err != nil {
		t.Fatalf("build request before cancel: %v", err)
	}
	response, err := unixHTTPClient(socketPath).Do(request)
	if err != nil {
		t.Fatalf("request before cancel: %v", err)
	}
	_ = response.Body.Close()

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected socket removed after shutdown, stat err=%v", err)
	}
}

func waitForSocket(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", socketPath)
}
