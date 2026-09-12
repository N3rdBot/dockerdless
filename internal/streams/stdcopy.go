// Package streams implements Docker-compatible stream framing, HTTP connection
// hijacking, TTY resize messages, and container log parsing.
package streams

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
)

// StreamType identifies one of the streams multiplexed by Docker's framing.
type StreamType = stdcopy.StdType

const (
	// Stdin is Docker's stdin stream id; StdCopy delivers it on stdout.
	Stdin StreamType = stdcopy.Stdin
	// Stdout is Docker's stdout stream id.
	Stdout StreamType = stdcopy.Stdout
	// Stderr is Docker's stderr stream id.
	Stderr StreamType = stdcopy.Stderr
	// Systemerr carries daemon-side errors; StdCopy returns it as an error.
	Systemerr StreamType = stdcopy.Systemerr
)

const (
	frameHeaderLen     = 8
	frameStreamIndex   = 0
	frameSizeIndex     = 4
	maxWriteChunk      = 1 << 20 // 1 MiB per frame, like Docker's writer
	DefaultMaxFrameLen = 32 << 20
)

var (
	// ErrFrameTooLarge is returned when a frame size field exceeds the cap.
	ErrFrameTooLarge = errors.New("streams: frame exceeds maximum size")
	// ErrUnknownStream is returned for a stream id outside Docker's set.
	ErrUnknownStream = errors.New("streams: unknown stream type")
	// ErrWriterUninitialized guards a zero-value Writer.
	ErrWriterUninitialized = errors.New("streams: writer is not initialized")
)

// Frame is one demultiplexed Docker stream frame.
type Frame struct {
	Stream StreamType
	Data   []byte
}

// Writer encapsulates writes to one stream in Docker's 8-byte framing:
// [stream_type(1)][0(3)][size(4)] big-endian, followed by the payload.
type Writer struct {
	w      io.Writer
	stream StreamType
}

// NewWriter returns a Writer that multiplexes its stream onto w.
func NewWriter(w io.Writer, stream StreamType) *Writer {
	return &Writer{w: w, stream: stream}
}

// Write emits p as one or more frames and returns the payload bytes consumed.
// A nil slice is a no-op, matching Moby's writer; an empty non-nil slice emits
// a zero-length frame, which Docker uses as a stream terminator.
func (w *Writer) Write(p []byte) (int, error) {
	if w == nil || w.w == nil {
		return 0, ErrWriterUninitialized
	}
	if p == nil {
		return 0, nil
	}
	if len(p) == 0 {
		_, err := w.writeChunk(nil)
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxWriteChunk {
			chunk = chunk[:maxWriteChunk]
		}
		n, err := w.writeChunk(chunk)
		written += n
		p = p[n:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (w *Writer) writeChunk(p []byte) (int, error) {
	buf := make([]byte, frameHeaderLen+len(p))
	buf[frameStreamIndex] = byte(w.stream)
	binary.BigEndian.PutUint32(buf[frameSizeIndex:], uint32(len(p)))
	copy(buf[frameHeaderLen:], p)

	n, err := w.w.Write(buf)
	switch {
	case err == nil && n == len(buf):
		return len(p), nil
	case err == nil && n < frameHeaderLen:
		return 0, io.ErrShortWrite
	case err == nil:
		return n - frameHeaderLen, io.ErrShortWrite
	case n < frameHeaderLen:
		return 0, err
	default:
		if partial := n - frameHeaderLen; partial < len(p) {
			return partial, err
		}
		return len(p), err
	}
}

// Reader demultiplexes Docker's framed stream one frame at a time.
type Reader struct {
	r      io.Reader
	header [frameHeaderLen]byte
}

// NewReader returns a Reader over framed wire bytes.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// ReadFrame returns the next frame. It returns io.EOF at a clean end of
// stream, io.ErrUnexpectedEOF for a truncated frame, and ErrFrameTooLarge or
// ErrUnknownStream for corrupt headers.
func (r *Reader) ReadFrame() (Frame, error) {
	if _, err := io.ReadFull(r.r, r.header[:]); err != nil {
		return Frame{}, err
	}
	stream := StreamType(r.header[frameStreamIndex])
	switch stream {
	case Stdin, Stdout, Stderr, Systemerr:
	default:
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownStream, stream)
	}
	size := int64(binary.BigEndian.Uint32(r.header[frameSizeIndex:]))
	if size > DefaultMaxFrameLen {
		return Frame{}, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r.r, data); err != nil {
		return Frame{}, err
	}
	return Frame{Stream: stream, Data: data}, nil
}

// Demux decodes src and writes stdout to dstOut and stderr to dstErr, with the
// same stream semantics as Moby's stdcopy.StdCopy: stdin goes to dstOut, a
// Systemerr frame aborts with an error, and a truncated trailing frame is
// treated as end of stream. The returned count is the payload bytes written.
func Demux(dstOut, dstErr io.Writer, src io.Reader) (int64, error) {
	r := NewReader(src)
	var written int64
	for {
		frame, err := r.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, nil
			}
			return written, err
		}
		if frame.Stream == Systemerr {
			return written, fmt.Errorf("streams: error from daemon in stream: %s", frame.Data)
		}
		dst := dstOut
		if frame.Stream == Stderr {
			dst = dstErr
		}
		n, err := dst.Write(frame.Data)
		written += int64(n)
		if err != nil {
			return written, err
		}
		if n != len(frame.Data) {
			return written, io.ErrShortWrite
		}
	}
}
