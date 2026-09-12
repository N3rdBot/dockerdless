package streams_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/streams"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestParseCRIEntry(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantErr   bool
		wantOut   bool
		wantFull  bool
		wantBody  string
		wantStamp string
	}{
		{
			name:      "stdout full line",
			line:      "2024-01-02T03:04:05.123456789Z stdout F hello world\n",
			wantOut:   true,
			wantFull:  true,
			wantBody:  "hello world\n",
			wantStamp: "2024-01-02T03:04:05.123456789Z",
		},
		{
			name:      "stderr partial line",
			line:      "2024-01-02T03:04:05Z stderr P chunk",
			wantFull:  false,
			wantBody:  "chunk",
			wantStamp: "2024-01-02T03:04:05Z",
		},
		{name: "plain raw line", line: "not a cri line\n", wantErr: true},
		{name: "bad timestamp", line: "yesterday stdout F nope\n", wantErr: true},
		{name: "bad stream", line: "2024-01-02T03:04:05Z syslog F nope\n", wantErr: true},
		{name: "missing tag", line: "2024-01-02T03:04:05Z stdout\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := streams.ParseCRIEntry([]byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCRIEntry(%q) succeeded, want error", tt.line)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCRIEntry(%q): %v", tt.line, err)
			}
			if entry.Partial != !tt.wantFull {
				t.Fatalf("Partial = %v, want %v", entry.Partial, !tt.wantFull)
			}
			gotStream := entry.Stream == streams.LogStreamStdout
			if gotStream != tt.wantOut {
				t.Fatalf("Stream = %q, want stdout=%v", entry.Stream, tt.wantOut)
			}
			if string(entry.Content) != tt.wantBody {
				t.Fatalf("Content = %q, want %q", entry.Content, tt.wantBody)
			}
			if got := entry.Timestamp.Format(time.RFC3339Nano); got != tt.wantStamp {
				t.Fatalf("Timestamp = %q, want %q", got, tt.wantStamp)
			}
		})
	}
}

const criFixture = `2024-01-02T03:04:05Z stdout F first
2024-01-02T03:04:06Z stderr F second
2024-01-02T03:04:07Z stdout F third
2024-01-02T03:04:08Z stdout P fourth
2024-01-02T03:04:08.5Z stdout F  continued
`

func TestReadLogsTailAndSince(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-json.log")
	if err := os.WriteFile(path, []byte(criFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	t.Run("tail 2", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := streams.ReadLogs(context.Background(), path, streams.LogOptions{Tail: 2}, &out, &errOut)
		if err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if out.String() != "fourth\n continued\n" {
			t.Fatalf("stdout = %q, want last two entries", out.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", errOut.String())
		}
	})

	t.Run("since", func(t *testing.T) {
		since, err := time.Parse(time.RFC3339Nano, "2024-01-02T03:04:07Z")
		if err != nil {
			t.Fatalf("parse since: %v", err)
		}
		var out, errOut bytes.Buffer
		if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{Since: since}, &out, &errOut); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if out.String() != "third\nfourth\n continued\n" {
			t.Fatalf("stdout = %q, want entries from 03:04:07 on", out.String())
		}
	})

	t.Run("timestamps", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{Tail: 1, Timestamps: true}, &out, &errOut); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if !strings.HasPrefix(out.String(), "2024-01-02T03:04:08.5Z ") {
			t.Fatalf("stdout = %q, want timestamp prefix", out.String())
		}
	})
}

func TestReadLogsRawFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.log")
	if err := os.WriteFile(path, []byte("plain line one\nplain line two\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{}, &out, &errOut); err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if out.String() != "plain line one\nplain line two\n" {
		t.Fatalf("stdout = %q", out.String())
	}
}

// TestReadLogsTruncatedLastLine proves a log file cut mid-write is handled
// without panicking and without dropping the complete lines before it.
func TestReadLogsTruncatedLastLine(t *testing.T) {
	t.Run("cri truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cri.log")
		content := criFixture + "2024-01-02T03:04:09Z stdout F truncated"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		var out, errOut bytes.Buffer
		if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{}, &out, &errOut); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if !strings.HasSuffix(out.String(), "truncated") {
			t.Fatalf("stdout = %q, want trailing truncated content", out.String())
		}
	})

	t.Run("raw truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.log")
		if err := os.WriteFile(path, []byte("complete\nincomplete"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		var out, errOut bytes.Buffer
		if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{}, &out, &errOut); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if out.String() != "complete\nincomplete" {
			t.Fatalf("stdout = %q", out.String())
		}
	})

	t.Run("tail ignores incomplete last line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tail.log")
		if err := os.WriteFile(path, []byte("a\nb\nc"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		var out, errOut bytes.Buffer
		if err := streams.ReadLogs(context.Background(), path, streams.LogOptions{Tail: 1}, &out, &errOut); err != nil {
			t.Fatalf("ReadLogs: %v", err)
		}
		if out.String() != "b\nc" {
			t.Fatalf("stdout = %q, want b plus the incomplete c", out.String())
		}
	})
}

// TestReadLogsMissingFileNoPanic proves a missing or rotated-away file yields
// an error instead of a panic.
func TestReadLogsMissingFileNoPanic(t *testing.T) {
	err := streams.ReadLogs(context.Background(), filepath.Join(t.TempDir(), "missing.log"), streams.LogOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("ReadLogs on a missing file returned nil error")
	}
}

// TestReadLogsFollowPicksUpNewLinesAndRotation proves follow mode streams new
// lines, survives rotation, and exits on context cancellation.
func TestReadLogsFollowPicksUpNewLinesAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "follow.log")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out, errOut syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- streams.ReadLogs(ctx, path, streams.LogOptions{Follow: true}, &out, &errOut)
	}()

	waitFor(t, 3*time.Second, func() bool { return strings.Contains(out.String(), "one\n") }, "initial line")

	appendFile(t, path, "two\n")
	waitFor(t, 3*time.Second, func() bool { return strings.Contains(out.String(), "two\n") }, "appended line")

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := os.WriteFile(path, []byte("three\n"), 0o644); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return strings.Contains(out.String(), "three\n") }, "line after rotation")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadLogs after cancel = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follow did not stop after context cancellation")
	}
}

func appendFile(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append: %v", err)
	}
}
