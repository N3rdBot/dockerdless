package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	socketMode           = 0o660
	socketDirectoryMode  = 0o755
	staleSocketDialWait  = 200 * time.Millisecond
	serverReadHeaderWait = 30 * time.Second
	serverIdleWait       = 120 * time.Second
)

// Server owns the daemon's Unix socket and HTTP lifecycle.
type Server struct {
	socketPath      string
	handler         http.Handler
	shutdownTimeout time.Duration

	mu       sync.Mutex
	listener net.Listener
	http     *http.Server
}

// ServerOption customizes the API server.
type ServerOption func(*Server)

// WithShutdownTimeout bounds graceful shutdown; zero keeps the default.
func WithShutdownTimeout(timeout time.Duration) ServerOption {
	return func(server *Server) {
		if timeout > 0 {
			server.shutdownTimeout = timeout
		}
	}
}

// NewServer validates the API boundary and stores the configured handler.
func NewServer(socketPath string, handler http.Handler, options ...ServerOption) (*Server, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("API socket path must not be empty")
	}
	if handler == nil {
		return nil, errors.New("API handler must not be nil")
	}
	server := &Server{
		socketPath:      socketPath,
		handler:         handler,
		shutdownTimeout: DefaultShutdownTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	return server, nil
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

// Listen binds the Unix socket. It creates the parent directory, removes a
// stale socket file (never a live one), listens, and applies 0660 permissions.
func (s *Server) Listen() (net.Listener, error) {
	if s == nil {
		return nil, errors.New("API server is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener, nil
	}
	if directory := filepath.Dir(s.socketPath); directory != "" {
		if err := os.MkdirAll(directory, socketDirectoryMode); err != nil {
			return nil, fmt.Errorf("create socket directory %s: %w", directory, err)
		}
	}
	if err := removeStaleSocket(s.socketPath); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", s.socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, socketMode); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set socket permissions on %s: %w", s.socketPath, err)
	}
	s.listener = listener
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: serverReadHeaderWait,
		IdleTimeout:       serverIdleWait,
	}
	return listener, nil
}

// ServeHTTP lets the server double as an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.handler == nil {
		http.NotFound(w, r)
		return
	}
	s.handler.ServeHTTP(w, r)
}

// Run serves HTTP until ctx is canceled, then shuts down gracefully with the
// configured timeout and removes the socket file.
func (s *Server) Run(ctx context.Context) error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	s.mu.Lock()
	server := s.http
	s.mu.Unlock()

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve API: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}

// Shutdown gracefully stops the HTTP server, closes the listener, and removes
// the socket file. It is safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.http
	listener := s.listener
	s.listener = nil
	s.mu.Unlock()

	var errs []error
	if server != nil {
		errs = append(errs, server.Shutdown(ctx))
	}
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove socket %s: %w", s.socketPath, err))
	}
	return errors.Join(errs...)
}

// removeStaleSocket unlinks a leftover socket file, but refuses to remove a
// live socket or a non-socket file.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path %s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, staleSocketDialWait)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("socket %s is already in use", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}
