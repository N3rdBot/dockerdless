package streams

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/moby/moby/api/types"
)

const (
	// MediaTypeRawStream is Docker's content type for an unframed TTY stream.
	MediaTypeRawStream = types.MediaTypeRawStream
	// MediaTypeMultiplexedStream is Docker's content type for framed streams.
	MediaTypeMultiplexedStream = types.MediaTypeMultiplexedStream
)

// ErrNotHijackable is returned when the response writer chain offers no
// http.Hijacker implementation.
var ErrNotHijackable = errors.New("streams: response writer does not support hijacking")

// Hijacker is the subset of http.Hijacker used here.
type Hijacker interface {
	Hijack() (net.Conn, *bufio.ReadWriter, error)
}

// Hijack walks ResponseWriter wrappers (those exposing Unwrap) until it finds
// one that can Hijack, so the observability middleware's status recorder does
// not break connection upgrades.
func Hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	for {
		if h, ok := w.(Hijacker); ok {
			return h.Hijack()
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, nil, ErrNotHijackable
		}
		next := unwrapper.Unwrap()
		if next == nil || next == w {
			return nil, nil, ErrNotHijackable
		}
		w = next
	}
}

// Upgrade hijacks the connection and writes Docker's upgrade response. When
// the request carries an Upgrade header the response is 101 with the stream
// content type (multiplexed frames for non-TTY API >= 1.42, raw otherwise);
// otherwise it is the 200 OK raw-stream handshake Docker clients accept.
func Upgrade(w http.ResponseWriter, r *http.Request, multiplexed bool, apiVersion string) (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := Hijack(w)
	if err != nil {
		return nil, nil, err
	}
	contentType := MediaTypeRawStream
	if multiplexed && apiVersionAtLeast(apiVersion, "1.42") {
		contentType = MediaTypeMultiplexedStream
	}
	var response string
	if r != nil && r.Header.Get("Upgrade") != "" {
		response = fmt.Sprintf("HTTP/1.1 101 UPGRADED\r\nContent-Type: %s\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", contentType)
	} else {
		response = fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n", MediaTypeRawStream)
	}
	if _, err := io.WriteString(conn, response); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("streams: writing upgrade response: %w", err)
	}
	return conn, rw, nil
}

func apiVersionAtLeast(version, minimum string) bool {
	if strings.TrimSpace(version) == "" {
		return true
	}
	current, ok := parseAPIVersion(version)
	if !ok {
		return false
	}
	floor, _ := parseAPIVersion(minimum)
	return current[0] > floor[0] || (current[0] == floor[0] && current[1] >= floor[1])
}

func parseAPIVersion(version string) ([2]int, bool) {
	var out [2]int
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 2)
	if len(parts) != 2 {
		return out, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return out, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return out, false
	}
	return [2]int{major, minor}, true
}

// ResizeMessage is the JSON payload Docker clients send to resize a TTY.
type ResizeMessage struct {
	Width  uint `json:"Width"`
	Height uint `json:"Height"`
}

// EncodeResize writes the Docker TTY resize message for width x height.
func EncodeResize(w io.Writer, width, height uint) error {
	return json.NewEncoder(w).Encode(ResizeMessage{Width: width, Height: height})
}

// DecodeResize reads one Docker TTY resize message.
func DecodeResize(r io.Reader) (ResizeMessage, error) {
	var msg ResizeMessage
	if err := json.NewDecoder(r).Decode(&msg); err != nil {
		return ResizeMessage{}, fmt.Errorf("streams: decoding resize message: %w", err)
	}
	return msg, nil
}

// Proxy copies data in both directions until both copies finish, half-closing
// each side when the connection supports it and ignoring the errors caused by
// tearing down the opposite direction.
func Proxy(a, b io.ReadWriteCloser) error {
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = copyHalfClose(a, b)
	}()
	go func() {
		defer wg.Done()
		errs[1] = copyHalfClose(b, a)
	}()
	wg.Wait()
	for _, err := range errs {
		if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
			continue
		}
		return err
	}
	return nil
}

func copyHalfClose(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	if closer, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	} else if closer, ok := dst.(io.Closer); ok {
		_ = closer.Close()
	}
	return err
}
