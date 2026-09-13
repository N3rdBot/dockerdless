package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/streams"
	"go.uber.org/zap"
)

const logFileMode = 0o640

// logSink owns the open log file and its CRI line writers for one container.
// Container output is re-encoded as CRI log lines so streams.ReadLogs decodes
// it with the same code path nerdctl/containerd log files use.
type logSink struct {
	file   *os.File
	stdout *criLogWriter
	stderr *criLogWriter
}

func (s *Service) logPath(id domain.ContainerID) string {
	return filepath.Join(s.logDir, string(id)+".log")
}

func (s *Service) openLogSink(id domain.ContainerID) (*logSink, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if sink, ok := s.logs[id]; ok {
		return sink, nil
	}
	if err := os.MkdirAll(s.logDir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.logPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		return nil, err
	}
	sink := &logSink{
		file:   file,
		stdout: newCRILogWriter(file, streams.LogStreamStdout),
		stderr: newCRILogWriter(file, streams.LogStreamStderr),
	}
	s.logs[id] = sink
	return sink, nil
}

func (s *Service) closeLogSink(id domain.ContainerID) {
	s.logMu.Lock()
	sink := s.logs[id]
	delete(s.logs, id)
	s.logMu.Unlock()
	if sink != nil {
		sink.close()
	}
}

func (s *Service) discardLogs(id domain.ContainerID) {
	s.closeLogSink(id)
	_ = os.Remove(s.logPath(id))
}

// CloseLogSinks closes every open log file and releases optional backend
// clients the service owns (currently the image config reader). It is the
// daemon shutdown hook.
func (s *Service) CloseLogSinks() {
	if s.closeImageConfigs != nil {
		if err := s.closeImageConfigs(); err != nil {
			s.logger.Warn("failed to close image config reader", zap.Error(err))
		}
		s.closeImageConfigs = nil
	}
	s.logMu.Lock()
	sinks := make([]*logSink, 0, len(s.logs))
	for id, sink := range s.logs {
		sinks = append(sinks, sink)
		delete(s.logs, id)
	}
	s.logMu.Unlock()
	for _, sink := range sinks {
		sink.close()
	}
}

func (s *logSink) stdoutWriter() io.Writer { return s.stdout }

func (s *logSink) stderrWriter() io.Writer { return s.stderr }

func (s *logSink) close() {
	_ = s.file.Close()
}

// criLogWriter renders container output as CRI log lines
// "<RFC3339Nano> <stdout|stderr> F <content>", flushing every write so logs
// appear while the container runs.
type criLogWriter struct {
	mu     sync.Mutex
	dst    io.Writer
	stream streams.LogStream
}

func newCRILogWriter(dst io.Writer, stream streams.LogStream) *criLogWriter {
	return &criLogWriter{dst: dst, stream: stream}
}

func (w *criLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	consumed := 0
	for len(p) > 0 {
		index := bytes.IndexByte(p, '\n')
		line := p
		if index >= 0 {
			line = p[:index]
		}
		if err := w.writeLine(line); err != nil {
			return consumed, err
		}
		if index < 0 {
			return len(p) + consumed, nil
		}
		p = p[index+1:]
		consumed += index + 1
	}
	return consumed, nil
}

func (w *criLogWriter) writeLine(line []byte) error {
	_, err := fmt.Fprintf(w.dst, "%s %s F %s\n", time.Now().Format(time.RFC3339Nano), w.stream, line)
	return err
}
