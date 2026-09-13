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
	"syscall"
	"time"

	"go.uber.org/zap"
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
	logger   *zap.Logger
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

// WithSocketLogger attaches a structured logger used for socket security
// decisions (repairs of unsafe stale sockets). A nil logger is ignored.
func WithSocketLogger(logger *zap.Logger) ServerOption {
	return func(server *Server) {
		if logger != nil {
			server.logger = logger
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
		logger:          zap.NewNop(),
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

// Listen binds the Unix socket. Security gate, in order:
//
//  1. A parent directory is created with 0755 when missing.
//  2. A path that exists but is not a socket is refused untouched.
//  3. A live socket is refused untouched.
//  4. A stale socket owned by another uid is refused untouched.
//  5. A stale socket with permissions beyond 0660 is tightened to 0660 (and
//     logged) before it is unlinked, so the exposed path never survives startup.
//  6. The freshly bound socket is re-verified as a 0660 socket owned by this
//     process; any mismatch closes it and refuses to serve.
func (s *Server) Listen() (net.Listener, error) {
	return s.listen(context.Background())
}

func (s *Server) listen(ctx context.Context) (net.Listener, error) {
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
	if err := s.removeStaleSocket(ctx); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", s.socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, socketMode); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set socket permissions on %s: %w", s.socketPath, err)
	}
	if err := verifySocketBinding(s.socketPath); err != nil {
		_ = listener.Close()
		return nil, err
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
	listener, err := s.listen(ctx)
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

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
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
// live socket, a non-socket file, or a socket owned by another uid. It repairs
// a stale socket with permissions wider than 0660 before unlinking it.
func (s *Server) removeStaleSocket(ctx context.Context) error {
	info, err := os.Lstat(s.socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect socket %s: %w", s.socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path %s exists and is not a socket", s.socketPath)
	}
	if err := probeLiveSocket(ctx, s.socketPath); err != nil {
		return err
	}
	if err := s.ensureSocketOwnership(info); err != nil {
		return err
	}
	if err := s.repairUnsafeSocketMode(info); err != nil {
		return err
	}
	if err := os.Remove(s.socketPath); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", s.socketPath, err)
	}
	return nil
}

// ensureSocketOwnership refuses to unlink a stale socket that this process does
// not own, so a foreign listener's filesystem state is never destroyed.
func (s *Server) ensureSocketOwnership(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := int(stat.Uid); uid != os.Geteuid() {
		return fmt.Errorf("refusing to remove socket %s owned by uid %d (daemon uid %d)", s.socketPath, uid, os.Geteuid())
	}
	return nil
}

// repairUnsafeSocketMode tightens a stale socket whose permissions grant
// anything beyond user and group read/write. The repair is best-effort and
// audited: if the mode cannot be tightened the daemon refuses to start rather
// than leaving an exposed socket behind.
func (s *Server) repairUnsafeSocketMode(info os.FileInfo) error {
	perm := info.Mode().Perm()
	if !unsafeSocketMode(perm) {
		return nil
	}
	if err := os.Chmod(s.socketPath, socketMode); err != nil {
		return fmt.Errorf("socket %s has unsafe permissions %04o and could not be repaired: %w", s.socketPath, perm, err)
	}
	s.logger.Warn("repaired unsafe socket permissions",
		zap.String("socket_path", s.socketPath),
		zap.String("previous_mode", fmt.Sprintf("%04o", perm)),
		zap.String("repaired_mode", fmt.Sprintf("%04o", socketMode)))
	return nil
}

// unsafeSocketMode reports whether mode grants access beyond 0660. A socket
// that is missing owner access is also unsafe: the daemon could not serve it.
func unsafeSocketMode(mode os.FileMode) bool {
	const safe = os.FileMode(socketMode)
	return mode&^safe != 0 || mode&0o600 != 0o600
}

// probeLiveSocket reports whether a socket at path accepts connections. A live
// socket is never removed; the daemon refuses to start instead.
func probeLiveSocket(ctx context.Context, path string) error {
	dialer := &net.Dialer{Timeout: staleSocketDialWait}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("socket %s is already in use", path)
	}
	return nil
}

// verifySocketBinding re-checks the freshly created socket: exactly 0660, a
// socket file, and owned by the daemon uid. The check makes the security
// guarantee independent of umask, listener creation path, and any later chmod.
func verifySocketBinding(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("verify socket %s: not a socket after bind", path)
	}
	if perm := info.Mode().Perm(); perm != socketMode {
		return fmt.Errorf("verify socket %s: permissions %04o, want %04o", path, perm, socketMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := int(stat.Uid); uid != os.Geteuid() {
		return fmt.Errorf("verify socket %s: owned by uid %d, want daemon uid %d", path, uid, os.Geteuid())
	}
	return nil
}
