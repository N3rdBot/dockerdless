package streams_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/streams"
)

type unwrapWriter struct {
	http.ResponseWriter
}

func (u unwrapWriter) Unwrap() http.ResponseWriter {
	return u.ResponseWriter
}

func readResponseHead(t *testing.T, br *bufio.Reader) (string, map[string]string) {
	t.Helper()
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading status line: %v", err)
	}
	headers := map[string]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading header line: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return strings.TrimRight(status, "\r\n"), headers
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("malformed header line %q", line)
		}
		headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
}

// TestUpgradeStatusAndContentType proves the 101 upgrade path works through a
// wrapping ResponseWriter (like the observability middleware) and selects the
// Docker content type per API version and TTY mode.
func TestUpgradeStatusAndContentType(t *testing.T) {
	tests := []struct {
		name            string
		upgrade         bool
		multiplexed     bool
		apiVersion      string
		wantStatus      string
		wantContentType string
	}{
		{"multiplexed", true, true, "1.44", "HTTP/1.1 101 UPGRADED", streams.MediaTypeMultiplexedStream},
		{"raw tty", true, false, "1.44", "HTTP/1.1 101 UPGRADED", streams.MediaTypeRawStream},
		{"old api is raw", true, true, "1.41", "HTTP/1.1 101 UPGRADED", streams.MediaTypeRawStream},
		{"no upgrade header is 200", false, true, "1.44", "HTTP/1.1 200 OK", streams.MediaTypeRawStream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, _, err := streams.Upgrade(unwrapWriter{w}, r, tt.multiplexed, tt.apiVersion); err != nil {
					t.Errorf("Upgrade: %v", err)
				}
			}))
			defer server.Close()

			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			upgrade := ""
			if tt.upgrade {
				upgrade = "Upgrade: tcp\r\nConnection: Upgrade\r\n"
			}
			if _, err := fmt.Fprintf(conn, "GET /attach HTTP/1.1\r\nHost: docker\r\n%s\r\n", upgrade); err != nil {
				t.Fatalf("write request: %v", err)
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			status, headers := readResponseHead(t, bufio.NewReader(conn))
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
			if headers["Content-Type"] != tt.wantContentType {
				t.Fatalf("Content-Type = %q, want %q", headers["Content-Type"], tt.wantContentType)
			}
			if tt.upgrade && (headers["Connection"] != "Upgrade" || headers["Upgrade"] != "tcp") {
				t.Fatalf("upgrade headers = %v, want Connection/Upgrade", headers)
			}
		})
	}
}

// TestHijackRequiresHijacker proves non-hijackable writers fail cleanly.
func TestHijackRequiresHijacker(t *testing.T) {
	_, _, err := streams.Hijack(unwrapWriter{http.ResponseWriter(nil)})
	if err == nil {
		t.Fatal("Hijack accepted an uninhabitable writer without error")
	}
}

// TestUpgradeAllowsBidirectionalExchange proves bytes flow both ways over a
// hijacked connection, the same way attach/exec will use it.
func TestUpgradeAllowsBidirectionalExchange(t *testing.T) {
	clientLine := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := streams.Upgrade(unwrapWriter{w}, r, true, "1.44")
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "server-hello\n"); err != nil {
			t.Errorf("server write: %v", err)
			return
		}
		line, err := rw.ReadString('\n')
		if err != nil {
			clientLine <- "read error: " + err.Error()
			return
		}
		clientLine <- line
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /exec HTTP/1.1\r\nHost: docker\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	status, _ := readResponseHead(t, br)
	if status != "HTTP/1.1 101 UPGRADED" {
		t.Fatalf("status = %q", status)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading server frame: %v", err)
	}
	if line != "server-hello\n" {
		t.Fatalf("server line = %q", line)
	}
	if _, err := io.WriteString(conn, "client-ping\n"); err != nil {
		t.Fatalf("client write: %v", err)
	}
	select {
	case got := <-clientLine:
		if got != "client-ping\n" {
			t.Fatalf("server received %q, want client-ping", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the client line")
	}
}

// TestResizeMessageCodec proves the TTY resize JSON is byte-exact and decodes.
func TestResizeMessageCodec(t *testing.T) {
	var buf bytes.Buffer
	if err := streams.EncodeResize(&buf, 120, 40); err != nil {
		t.Fatalf("EncodeResize: %v", err)
	}
	if got, want := buf.String(), "{\"Width\":120,\"Height\":40}\n"; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
	msg, err := streams.DecodeResize(bytes.NewReader([]byte(`{"Width":200,"Height":100}`)))
	if err != nil {
		t.Fatalf("DecodeResize: %v", err)
	}
	if msg.Width != 200 || msg.Height != 100 {
		t.Fatalf("decoded = %+v, want 200x100", msg)
	}
}

// TestProxyBidirectional proves the hijack proxy bridges both directions and
// terminates when both sides close.
func TestProxyBidirectional(t *testing.T) {
	clientApp, clientProxy := net.Pipe()
	serverApp, serverProxy := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- streams.Proxy(clientProxy, serverProxy) }()

	writeErr := make(chan error, 1)
	go func() {
		_, err := clientApp.Write([]byte("to-server"))
		writeErr <- err
	}()
	got := make([]byte, len("to-server"))
	serverApp.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(serverApp, got); err != nil {
		t.Fatalf("reading server side: %v", err)
	}
	if string(got) != "to-server" {
		t.Fatalf("server got %q", got)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("client write: %v", err)
	}

	go func() {
		_, err := serverApp.Write([]byte("to-client"))
		writeErr <- err
	}()
	back := make([]byte, len("to-client"))
	clientApp.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(clientApp, back); err != nil {
		t.Fatalf("reading client side: %v", err)
	}
	if string(back) != "to-client" {
		t.Fatalf("client got %q", back)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("server write: %v", err)
	}

	clientApp.Close()
	serverApp.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Proxy returned %v, want nil after clean close", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Proxy did not terminate after both sides closed")
	}
}
