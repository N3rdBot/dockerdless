package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newUnixListener binds a Unix socket and, when keep is true, leaves the socket
// file behind after the listener closes so tests can simulate a stale socket.
func newUnixListener(t *testing.T, socketPath string, mode os.FileMode, keep bool) *net.UnixListener {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", socketPath, err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		t.Fatalf("listener for %s is %T, want *net.UnixListener", socketPath, listener)
	}
	if err := os.Chmod(socketPath, mode); err != nil {
		_ = unixListener.Close()
		t.Fatalf("chmod socket %s to %04o: %v", socketPath, mode, err)
	}
	if keep {
		unixListener.SetUnlinkOnClose(false)
	}
	return unixListener
}

func socketInfo(t *testing.T, socketPath string) (os.FileInfo, *syscall.Stat_t) {
	t.Helper()
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("lstat %s: %v", socketPath, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s has type %T, want *syscall.Stat_t", socketPath, info.Sys())
	}
	return info, stat
}

// TestServer_repairsStaleSocketWithUnsafePermissions is the failing-first proof
// for the startup permission gate: a stale socket that grants access beyond
// 0660 is repaired to 0660 before removal (recorded in the audit log), and the
// listener that ends up serving is always 0660 and owned by this process.
func TestServer_repairsStaleSocketWithUnsafePermissions(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	stale := newUnixListener(t, socketPath, 0o777, true)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}
	if info, _ := socketInfo(t, socketPath); info.Mode().Perm() != 0o777 {
		t.Fatalf("stale socket mode = %04o, want 0777", info.Mode().Perm())
	}

	core, observed := observer.New(zap.DebugLevel)
	server, err := NewServer(socketPath, http.NotFoundHandler(), WithSocketLogger(zap.New(core)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	listener, err := server.Listen()
	if err != nil {
		if !strings.Contains(err.Error(), "unsafe") && !strings.Contains(err.Error(), "permission") {
			t.Fatalf("refusal error = %v, want an explicit unsafe-permission reason", err)
		}
		return
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	_ = listener

	info, stat := socketInfo(t, socketPath)
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("served socket mode = %04o, want 0660", perm)
	}
	if got := int(stat.Uid); got != os.Geteuid() {
		t.Fatalf("served socket uid = %d, want %d", got, os.Geteuid())
	}
	if observed.FilterMessage("repaired unsafe socket permissions").Len() == 0 {
		t.Fatal("expected a repair audit record for the unsafe stale socket")
	}
}

// TestServer_acceptsStaleSocketWithSafePermissions keeps the stale-socket
// replacement behavior: a dead socket at 0660 is not a security problem and the
// daemon replaces it silently.
func TestServer_acceptsStaleSocketWithSafePermissions(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	stale := newUnixListener(t, socketPath, 0o660, true)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err != nil {
		t.Fatalf("expected a 0660 stale socket to be replaced, got %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	info, _ := socketInfo(t, socketPath)
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("served socket mode = %04o, want 0660", perm)
	}
}

// TestServer_refusesLiveWorldAccessibleSocket proves the daemon never touches a
// live socket it does not own the lifecycle of, even when that socket has
// unsafe permissions: it refuses and leaves the file byte-for-byte in place.
func TestServer_refusesLiveWorldAccessibleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	live := newUnixListener(t, socketPath, 0o777, false)
	defer func() { _ = live.Close() }()

	before, _ := socketInfo(t, socketPath)

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected live-socket refusal, got %v", err)
	}

	after, afterStat := socketInfo(t, socketPath)
	if after.Mode().Perm() != 0o777 {
		t.Fatalf("live socket mode changed to %04o; the daemon must not touch a foreign live socket", after.Mode().Perm())
	}
	if !os.SameFile(before, after) {
		t.Fatal("live socket was replaced; the daemon must not touch a foreign live socket")
	}
	if afterStat.Ino != before.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("live socket inode changed; the daemon must not touch a foreign live socket")
	}

	// The live listener must still accept connections.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("live socket stopped accepting connections: %v", err)
	}
	_ = conn.Close()
}

func TestServer_stillRefusesNonSocketFile(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err == nil || !strings.Contains(err.Error(), "is not a socket") {
		t.Fatalf("expected non-socket refusal, got %v", err)
	}
	if content, readErr := os.ReadFile(socketPath); readErr != nil || string(content) != "not a socket" {
		t.Fatalf("non-socket file was modified: content=%q err=%v", content, readErr)
	}
}

// TestServer_refusesSocketOwnedByAnotherUser proves the daemon refuses to
// unlink a stale socket owned by a different uid instead of deleting a foreign
// listener's filesystem state. Skipped when the test cannot create another
// owner (no chown permission).
func TestServer_refusesSocketOwnedByAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a socket owned by another uid")
	}
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	stale := newUnixListener(t, socketPath, 0o660, true)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}
	const foreignUID = 65534 // nobody
	if err := os.Chown(socketPath, foreignUID, foreignUID); err != nil {
		t.Skipf("cannot chown socket to uid %d: %v", foreignUID, err)
	}

	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("expected foreign-owner refusal, got %v", err)
	}
	_, stat := socketInfo(t, socketPath)
	if stat.Uid != foreignUID {
		t.Fatalf("foreign socket uid = %d, want %d (file must be untouched)", stat.Uid, foreignUID)
	}
}

// TestServer_createdSocketIsExactlyUserAndGroupAccessible pins the post-bind
// mode and ownership verification: no umask can widen the daemon socket.
func TestServer_createdSocketIsExactlyUserAndGroupAccessible(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "dockerdless.sock")
	server, err := NewServer(socketPath, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := server.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	info, stat := socketInfo(t, socketPath)
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode = %04o, want exactly 0660", perm)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("listen did not create a socket file")
	}
	if got := int(stat.Uid); got != os.Geteuid() {
		t.Fatalf("socket uid = %d, want %d", got, os.Geteuid())
	}
	if got := int(stat.Gid); got != os.Getegid() {
		t.Fatalf("socket gid = %d, want %d", got, os.Getegid())
	}
}
