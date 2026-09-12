package streams

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// LogFormat selects how a log file is decoded.
type LogFormat int

const (
	// LogFormatAuto decodes CRI lines and falls back to raw passthrough.
	LogFormatAuto LogFormat = iota
	// LogFormatCRI decodes CRI lines and skips anything else.
	LogFormatCRI
	// LogFormatRaw passes every line through untouched.
	LogFormatRaw
)

// LogStream is the stream a decoded log entry belongs to.
type LogStream string

const (
	// LogStreamStdout carries container stdout.
	LogStreamStdout LogStream = "stdout"
	// LogStreamStderr carries container stderr.
	LogStreamStderr LogStream = "stderr"
)

const (
	defaultLogPollInterval = 50 * time.Millisecond
	tailReadBlockSize      = 32 * 1024
)

// LogOptions filters a log file while reading it.
type LogOptions struct {
	// Follow keeps reading after EOF, waiting for new lines and rotation.
	Follow bool
	// Tail limits the read to the last N complete lines; 0 reads everything.
	Tail int
	// Since drops timestamped entries older than this.
	Since time.Time
	// Until stops at the first timestamped entry newer than this.
	Until time.Time
	// Timestamps prefixes each timestamped entry with its RFC3339Nano time.
	Timestamps bool
	// Format selects CRI, raw, or auto detection.
	Format LogFormat
	// PollInterval overrides the follow polling cadence (default 50ms).
	PollInterval time.Duration
}

// LogEntry is one decoded log line.
type LogEntry struct {
	Timestamp time.Time
	Stream    LogStream
	Partial   bool
	Content   []byte
}

// ParseCRIEntry decodes one CRI/nerdctl log line:
// <RFC3339Nano> <stdout|stderr> <P|F> <content>.
func ParseCRIEntry(line []byte) (LogEntry, error) {
	timestamp, rest, ok := cutToken(line)
	if !ok {
		return LogEntry{}, errors.New("streams: CRI line missing timestamp")
	}
	ts, err := time.Parse(time.RFC3339Nano, string(timestamp))
	if err != nil {
		return LogEntry{}, fmt.Errorf("streams: CRI timestamp %q: %w", timestamp, err)
	}
	stream, rest, ok := cutToken(rest)
	if !ok {
		return LogEntry{}, errors.New("streams: CRI line missing stream")
	}
	if string(stream) != string(LogStreamStdout) && string(stream) != string(LogStreamStderr) {
		return LogEntry{}, fmt.Errorf("streams: CRI line has unexpected stream %q", stream)
	}
	tag, content, ok := cutToken(rest)
	if !ok {
		return LogEntry{}, errors.New("streams: CRI line missing log tag")
	}
	return LogEntry{
		Timestamp: ts,
		Stream:    LogStream(stream),
		Partial:   strings.SplitN(string(tag), ":", 2)[0] == "P",
		Content:   content,
	}, nil
}

func cutToken(line []byte) (token, rest []byte, ok bool) {
	before, after, found := bytes.Cut(line, []byte(" "))
	if !found {
		return nil, nil, false
	}
	return before, after, true
}

// FindTailStart returns the byte offset where the last n complete lines begin.
// An incomplete trailing line (one without a newline) is not counted, matching
// Kubernetes/nerdctl tail semantics.
func FindTailStart(f io.ReadSeeker, n int64) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	wanted := n + 1
	var seen int64
	buf := make([]byte, tailReadBlockSize)
	offset := size
	for offset > 0 {
		readLen := min(int64(len(buf)), offset)
		start := offset - readLen
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err := io.ReadFull(f, buf[:readLen]); err != nil {
			return 0, err
		}
		chunk := buf[:readLen]
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] != '\n' {
				continue
			}
			seen++
			if seen == wanted {
				return start + int64(i) + 1, nil
			}
		}
		offset = start
	}
	return 0, nil
}

// ReadLogs streams a container log file to stdout and stderr. A missing file
// is an error unless Follow is set, in which case it is awaited. Rotation is
// detected by inode change or truncation, and context cancellation ends a
// follow cleanly.
func ReadLogs(ctx context.Context, path string, opts LogOptions, stdout, stderr io.Writer) error {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultLogPollInterval
	}
	var (
		started  bool
		offset   int64
		lastStat os.FileInfo
	)
	for {
		if ctx.Err() != nil {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			if !opts.Follow {
				return fmt.Errorf("streams: opening log file %q: %w", path, err)
			}
			if sleepErr := sleepCtx(ctx, opts.PollInterval); sleepErr != nil {
				return nil
			}
			continue
		}
		stat, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return fmt.Errorf("streams: stat %q: %w", path, statErr)
		}
		if !started {
			offset, err = FindTailStart(file, int64(opts.Tail))
			if err != nil {
				_ = file.Close()
				return fmt.Errorf("streams: tailing %q: %w", path, err)
			}
			started = true
		} else if lastStat == nil || !os.SameFile(lastStat, stat) || stat.Size() < offset {
			offset = 0
		}
		lastStat = stat

		next, done, err := emitLogLines(file, offset, opts, stdout, stderr)
		_ = file.Close()
		offset = next
		if err != nil {
			return err
		}
		if done || !opts.Follow {
			return nil
		}
		if sleepErr := sleepCtx(ctx, opts.PollInterval); sleepErr != nil {
			return nil
		}
	}
}

func emitLogLines(file *os.File, offset int64, opts LogOptions, stdout, stderr io.Writer) (int64, bool, error) {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, false, fmt.Errorf("streams: seeking log file: %w", err)
	}
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			complete := line[len(line)-1] == '\n'
			if !complete && opts.Follow {
				break
			}
			entry, ok := decodeLogLine(line, opts.Format)
			if ok {
				if done := entryOutOfRange(entry, opts); done {
					return offset, true, nil
				}
				if !entryTooOld(entry, opts) {
					if writeErr := writeLogEntry(entry, opts, stdout, stderr); writeErr != nil {
						return offset, false, writeErr
					}
				}
			}
			offset += int64(len(line))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return offset, false, nil
			}
			return offset, false, fmt.Errorf("streams: reading log file: %w", err)
		}
	}
	return offset, false, nil
}

func decodeLogLine(line []byte, format LogFormat) (LogEntry, bool) {
	switch format {
	case LogFormatRaw:
		return LogEntry{Stream: LogStreamStdout, Content: line}, true
	case LogFormatCRI:
		entry, err := ParseCRIEntry(line)
		return entry, err == nil
	default:
		if entry, err := ParseCRIEntry(line); err == nil {
			return entry, true
		}
		return LogEntry{Stream: LogStreamStdout, Content: line}, true
	}
}

func entryTooOld(entry LogEntry, opts LogOptions) bool {
	return !opts.Since.IsZero() && !entry.Timestamp.IsZero() && entry.Timestamp.Before(opts.Since)
}

func entryOutOfRange(entry LogEntry, opts LogOptions) bool {
	return !opts.Until.IsZero() && !entry.Timestamp.IsZero() && entry.Timestamp.After(opts.Until)
}

func writeLogEntry(entry LogEntry, opts LogOptions, stdout, stderr io.Writer) error {
	dst := stdout
	if entry.Stream == LogStreamStderr {
		dst = stderr
	}
	if opts.Timestamps && !entry.Timestamp.IsZero() {
		if _, err := io.WriteString(dst, entry.Timestamp.Format(time.RFC3339Nano)+" "); err != nil {
			return err
		}
	}
	if _, err := dst.Write(entry.Content); err != nil {
		return err
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
