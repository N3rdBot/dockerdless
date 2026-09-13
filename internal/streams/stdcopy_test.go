package streams_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"

	"github.com/N3rdBot/dockerdless/internal/streams"
)

type refFrame struct {
	Stream streams.StreamType
	Data   []byte
}

// referenceFrame encodes one Docker multiplexed frame using the wire format
// documented by Moby ([stream_type(1)][0(3)][size(4)] big-endian). It is an
// independent encoder on purpose: it must not share code with internal/streams
// so that the reader is tested against the specification, not against itself.
func referenceFrame(stream streams.StreamType, payload []byte) []byte {
	var hdr [8]byte
	hdr[0] = byte(stream)
	//nolint:gosec // G115: test payloads are small fixtures read into memory.
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr[:], payload...)
}

func referenceStream(t *testing.T, frames ...refFrame) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, f := range frames {
		buf.Write(referenceFrame(f.Stream, f.Data))
	}
	return buf.Bytes()
}

// TestWriterIsDecodedByMobyStdCopy proves the writer produces bytes that
// Moby's own demultiplexer accepts for both stdout and stderr.
func TestWriterIsDecodedByMobyStdCopy(t *testing.T) {
	var wire bytes.Buffer
	stdout := streams.NewWriter(&wire, streams.Stdout)
	stderr := streams.NewWriter(&wire, streams.Stderr)

	stdoutPayload := []byte("hello from stdout\n")
	stderrPayload := []byte("boom from stderr\n")

	n1, err := stdout.Write(stdoutPayload)
	if err != nil {
		t.Fatalf("stdout write: %v", err)
	}
	if n1 != len(stdoutPayload) {
		t.Fatalf("stdout write returned %d, want %d", n1, len(stdoutPayload))
	}
	n2, err := stderr.Write(stderrPayload)
	if err != nil {
		t.Fatalf("stderr write: %v", err)
	}
	if n2 != len(stderrPayload) {
		t.Fatalf("stderr write returned %d, want %d", n2, len(stderrPayload))
	}

	var outBuf, errBuf bytes.Buffer
	written, err := stdcopy.StdCopy(&outBuf, &errBuf, &wire)
	if err != nil {
		t.Fatalf("moby StdCopy rejected our frames: %v", err)
	}
	if got, want := written, int64(len(stdoutPayload)+len(stderrPayload)); got != want {
		t.Fatalf("StdCopy written = %d, want %d", got, want)
	}
	if outBuf.String() != string(stdoutPayload) {
		t.Fatalf("stdout = %q, want %q", outBuf.String(), stdoutPayload)
	}
	if errBuf.String() != string(stderrPayload) {
		t.Fatalf("stderr = %q, want %q", errBuf.String(), stderrPayload)
	}
}

// TestWriterEmptyPayloadIsDecodedByMoby proves zero-length frames (used by
// Docker as stream terminators) survive the round-trip, while a nil write is
// a no-op like Moby's writer.
func TestWriterEmptyPayloadIsDecodedByMoby(t *testing.T) {
	var wire bytes.Buffer
	w := streams.NewWriter(&wire, streams.Stdout)
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if wire.Len() != 0 {
		t.Fatalf("Write(nil) wrote %d bytes, want 0", wire.Len())
	}
	if _, err := w.Write([]byte{}); err != nil {
		t.Fatalf("write empty frame: %v", err)
	}
	if wire.Len() != 8 {
		t.Fatalf("wire length = %d, want 8-byte header only", wire.Len())
	}
	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, &wire); err != nil {
		t.Fatalf("moby StdCopy rejected empty frame: %v", err)
	}
	if outBuf.Len() != 0 || errBuf.Len() != 0 {
		t.Fatalf("empty frame produced output: out=%q err=%q", outBuf.String(), errBuf.String())
	}
}

// TestReaderDecodesReferenceFrames proves the reader accepts frames produced
// by the specification-level encoder (the "vice-versa" direction).
func TestReaderDecodesReferenceFrames(t *testing.T) {
	frames := []refFrame{
		{streams.Stdout, []byte("out-1")},
		{streams.Stderr, []byte("err-1")},
		{streams.Stdin, []byte("in-1")},
		{streams.Systemerr, []byte("panic: daemon")},
		{streams.Stdout, nil},
	}
	wire := referenceStream(t, frames...)

	r := streams.NewReader(bytes.NewReader(wire))
	for i, want := range frames {
		frame, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if frame.Stream != want.Stream {
			t.Fatalf("frame %d: stream = %d, want %d", i, frame.Stream, want.Stream)
		}
		if !bytes.Equal(frame.Data, want.Data) {
			t.Fatalf("frame %d: data = %q, want %q", i, frame.Data, want.Data)
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame at end = %v, want io.EOF", err)
	}
}

// TestDemuxMatchesMobyOnGeneratedStreams feeds the same wire bytes to our
// demultiplexer and Moby's and requires byte-identical output.
func TestDemuxMatchesMobyOnGeneratedStreams(t *testing.T) {
	//nolint:gosec // G404: deterministic payload generator for a framing test, not security-sensitive.
	rng := rand.New(rand.NewSource(42))
	var wire bytes.Buffer
	var wantOut, wantErr bytes.Buffer
	for range 64 {
		//nolint:gosec // G115: rng.Intn(2) yields 0 or 1, so the StreamType is 1 or 2.
		stream := streams.StreamType(1 + rng.Intn(2))
		payload := make([]byte, rng.Intn(2048))
		if _, err := rng.Read(payload); err != nil {
			t.Fatalf("rng: %v", err)
		}
		wire.Write(referenceFrame(stream, payload))
		if stream == streams.Stdout {
			wantOut.Write(payload)
		} else {
			wantErr.Write(payload)
		}
	}

	var gotOut, gotErr bytes.Buffer
	gotWritten, err := streams.Demux(&gotOut, &gotErr, bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatalf("Demux: %v", err)
	}

	var mobyOut, mobyErr bytes.Buffer
	mobyWritten, err := stdcopy.StdCopy(&mobyOut, &mobyErr, bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if gotWritten != mobyWritten {
		t.Fatalf("written = %d, want %d (moby)", gotWritten, mobyWritten)
	}
	if !bytes.Equal(gotOut.Bytes(), wantOut.Bytes()) || !bytes.Equal(gotErr.Bytes(), wantErr.Bytes()) {
		t.Fatalf("demux output differs from expected streams (out %d vs %d, err %d vs %d)",
			gotOut.Len(), wantOut.Len(), gotErr.Len(), wantErr.Len())
	}
	if gotOut.String() != mobyOut.String() || gotErr.String() != mobyErr.String() {
		t.Fatalf("demux output differs from moby: out=%q/%q err=%q/%q",
			gotOut.String(), mobyOut.String(), gotErr.String(), mobyErr.String())
	}
}

// TestDemuxSurfacesSystemerr proves a Systemerr frame terminates demuxing with
// an error, like Moby.
func TestDemuxSurfacesSystemerr(t *testing.T) {
	wire := referenceStream(t,
		refFrame{streams.Stdout, []byte("before")},
		refFrame{streams.Systemerr, []byte("daemon exploded")},
	)
	var out, errBuf bytes.Buffer
	if _, err := streams.Demux(&out, &errBuf, bytes.NewReader(wire)); err == nil {
		t.Fatal("Demux accepted a Systemerr frame without error")
	}
	if out.String() != "before" {
		t.Fatalf("stdout before systemerr = %q, want %q", out.String(), "before")
	}
}

// TestReaderTruncatedFrameDoesNotPanic covers a partially written header and a
// partially written payload, including a truncated final line (the log-file
// equivalent) being handed to the frame reader.
func TestReaderTruncatedFrameDoesNotPanic(t *testing.T) {
	t.Run("partial header", func(t *testing.T) {
		r := streams.NewReader(bytes.NewReader([]byte{0x01, 0x00}))
		if _, err := r.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ReadFrame = %v, want io.ErrUnexpectedEOF", err)
		}
	})
	t.Run("partial payload", func(t *testing.T) {
		wire := referenceFrame(streams.Stdout, []byte("0123456789"))
		r := streams.NewReader(bytes.NewReader(wire[:12]))
		if _, err := r.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ReadFrame = %v, want io.ErrUnexpectedEOF", err)
		}
	})
	t.Run("demux tolerates truncated tail like moby", func(t *testing.T) {
		wire := referenceFrame(streams.Stdout, []byte("complete"))
		wire = append(wire, referenceFrame(streams.Stderr, []byte("half"))[:10]...)
		var out, errBuf bytes.Buffer
		written, err := streams.Demux(&out, &errBuf, bytes.NewReader(wire))
		if err != nil {
			t.Fatalf("Demux on truncated tail: %v", err)
		}
		if written != int64(len("complete")) || out.String() != "complete" {
			t.Fatalf("Demux wrote %d bytes (%q), want %d", written, out.String(), len("complete"))
		}
	})
}

// TestReaderRejectsOversizedFrame proves a corrupt size field cannot trigger a
// multi-gigabyte allocation.
func TestReaderRejectsOversizedFrame(t *testing.T) {
	var hdr [8]byte
	hdr[0] = byte(streams.Stdout)
	binary.BigEndian.PutUint32(hdr[4:], ^uint32(0))
	r := streams.NewReader(bytes.NewReader(hdr[:]))
	_, err := r.ReadFrame()
	if !errors.Is(err, streams.ErrFrameTooLarge) {
		t.Fatalf("ReadFrame = %v, want ErrFrameTooLarge", err)
	}
}

// TestReaderRejectsUnknownStream proves a corrupt stream type is rejected.
func TestReaderRejectsUnknownStream(t *testing.T) {
	wire := referenceFrame(7, []byte("garbage"))
	r := streams.NewReader(bytes.NewReader(wire))
	if _, err := r.ReadFrame(); !errors.Is(err, streams.ErrUnknownStream) {
		t.Fatalf("ReadFrame = %v, want ErrUnknownStream", err)
	}
}

// shortWriter accepts at most n bytes per Write call.
type shortWriter struct {
	limit int
	buf   bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.buf.Write(p)
}

// TestWriterShortWriteHasNoNegativeCounts proves a short underlying write is
// reported safely.
func TestWriterShortWriteHasNoNegativeCounts(t *testing.T) {
	for _, limit := range []int{2, 7, 8} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			sw := &shortWriter{limit: limit}
			n, err := streams.NewWriter(sw, streams.Stdout).Write([]byte("payload"))
			if n < 0 {
				t.Fatalf("Write returned negative count %d", n)
			}
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("Write error = %v, want io.ErrShortWrite", err)
			}
		})
	}
}
