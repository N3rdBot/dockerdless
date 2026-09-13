package streams

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const benchmarkFramePayload = 4 << 10 // 4 KiB, one typical read

func benchmarkPayload(size int) []byte {
	const pattern = "dockerdless benchmark payload "
	source := []byte(pattern)
	payload := make([]byte, 0, size)
	for len(payload) < size {
		payload = append(payload, source...)
	}
	return payload[:size]
}

func benchmarkFrame(payload []byte) []byte {
	var buf bytes.Buffer
	writer := NewWriter(&buf, Stdout)
	if _, err := writer.Write(payload); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// BenchmarkWriterWrite measures the hot path of framing daemon output, which
// runs once per container-log chunk.
func BenchmarkWriterWrite(b *testing.B) {
	payload := benchmarkPayload(benchmarkFramePayload)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		writer := NewWriter(io.Discard, Stdout)
		if _, err := writer.Write(payload); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// BenchmarkWriterWriteLargeChunk measures the 1 MiB chunking path exercised by
// large log writes.
func BenchmarkWriterWriteLargeChunk(b *testing.B) {
	payload := bytes.Repeat([]byte{'x'}, maxWriteChunk+1)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		writer := NewWriter(io.Discard, Stdout)
		if _, err := writer.Write(payload); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// BenchmarkReadFrame measures per-frame demultiplexing cost with a fixed
// pre-framed buffer reset on every iteration.
func BenchmarkReadFrame(b *testing.B) {
	payload := benchmarkPayload(benchmarkFramePayload)
	framed := benchmarkFrame(payload)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		reader := NewReader(bytes.NewReader(framed))
		frame, err := reader.ReadFrame()
		if err != nil {
			b.Fatalf("ReadFrame: %v", err)
		}
		if len(frame.Data) != len(payload) {
			b.Fatalf("frame payload = %d bytes, want %d", len(frame.Data), len(payload))
		}
	}
}

// BenchmarkDemux measures the end-to-end decode loop of a multiplexed stream,
// the path used for exec and attach responses.
func BenchmarkDemux(b *testing.B) {
	const frames = 32
	payload := bytes.Repeat([]byte("demux payload "), 128)
	var wire bytes.Buffer
	for index := range frames {
		stream := Stdout
		if index%2 == 1 {
			stream = Stderr
		}
		if _, err := NewWriter(&wire, stream).Write(payload); err != nil {
			b.Fatalf("frame %d: %v", index, err)
		}
	}
	encoded := wire.Bytes()
	b.SetBytes(int64(len(encoded)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var stdout, stderr bytes.Buffer
		if _, err := Demux(&stdout, &stderr, bytes.NewReader(encoded)); err != nil {
			b.Fatalf("Demux: %v", err)
		}
		if got := stdout.Len() + stderr.Len(); got != frames*len(payload) {
			b.Fatalf("demuxed %d bytes, want %d", got, frames*len(payload))
		}
	}
}

// BenchmarkParseCRIEntry measures one CRI log-line decode, the inner loop of
// every `docker logs` follow.
func BenchmarkParseCRIEntry(b *testing.B) {
	line := []byte("2026-09-13T10:11:12.123456789Z stdout F container log line payload")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ParseCRIEntry(line); err != nil {
			b.Fatalf("ParseCRIEntry: %v", err)
		}
	}
}

// BenchmarkParseCRIEntryPartial measures the partial-tag branch (`P`), which
// carries an extra SplitN above the plain tag path.
func BenchmarkParseCRIEntryPartial(b *testing.B) {
	line := []byte("2026-09-13T10:11:12.123456789Z stderr P fragmented log line payload")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ParseCRIEntry(line); err != nil {
			b.Fatalf("ParseCRIEntry: %v", err)
		}
	}
}

// BenchmarkFindTailStart measures the backward tail scan over a 256 KiB file
// with 4096 log lines, the cost paid once per `docker logs --tail`.
func BenchmarkFindTailStart(b *testing.B) {
	path := filepath.Join(b.TempDir(), "container.log")
	var content bytes.Buffer
	for index := range 4096 {
		fmt.Fprintf(&content, "2026-09-13T10:11:12.123456789Z stdout F line %d\n", index)
	}
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		b.Fatalf("write log file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		b.Fatalf("open log file: %v", err)
	}
	defer func() { _ = file.Close() }()

	b.SetBytes(int64(content.Len()))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := FindTailStart(file, 100); err != nil {
			b.Fatalf("FindTailStart: %v", err)
		}
	}
}

// BenchmarkDecodeLogLineAutoFallback measures the auto-format decode of a
// non-CRI line, which pays both the CRI parse attempt and the raw fallback.
func BenchmarkDecodeLogLineAutoFallback(b *testing.B) {
	line := []byte("plain container output without any CRI prefix\n")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		entry, ok := decodeLogLine(line, LogFormatAuto)
		if !ok || entry.Stream != LogStreamStdout {
			b.Fatal("auto decode did not fall back to stdout")
		}
	}
}
