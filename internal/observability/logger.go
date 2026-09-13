// Package observability provides logging and telemetry bootstrap helpers.
package observability

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	otellog "go.opentelemetry.io/otel/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// instrumentationScope names the instrumentation library that emits the
// daemon's records.
const instrumentationScope = "github.com/N3rdBot/dockerdless"

// Logger wraps a zap logger with the atomic level that filters it. The wrapped
// *zap.Logger methods are promoted, so Logger remains a drop-in for callers
// that already use zap.
type Logger struct {
	*zap.Logger

	level zap.AtomicLevel
}

// LoggerOption configures NewLogger.
type LoggerOption func(*loggerConfig)

type loggerConfig struct {
	output io.Writer
	bridge otellog.LoggerProvider
}

// WithLogOutput redirects structured log output. It defaults to stderr.
func WithLogOutput(w io.Writer) LoggerOption {
	return func(cfg *loggerConfig) {
		if w != nil {
			cfg.output = w
		}
	}
}

// WithLogBridge tees log records through the OpenTelemetry log pipeline using
// the otelzap bridge, which lets exported records carry the active span's trace
// and span ids when a record is emitted with ContextField.
func WithLogBridge(provider otellog.LoggerProvider) LoggerOption {
	return func(cfg *loggerConfig) {
		cfg.bridge = provider
	}
}

// NewLogger builds the daemon's structured logger. The returned Logger exposes
// its AtomicLevel, so callers can change filtering at runtime without
// rebuilding the logger.
func NewLogger(level string, opts ...LoggerOption) (*Logger, error) {
	cfg := loggerConfig{output: os.Stderr}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	logLevel, err := parseLogLevel(level)
	if err != nil {
		return nil, err
	}

	core := newLoggerCore(logLevel, cfg.output, cfg.bridge)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	return &Logger{Logger: logger, level: logLevel}, nil
}

// Level returns the live atomic level. Mutating it through SetLevel changes
// filtering immediately without rebuilding the logger.
func (l *Logger) Level() zap.AtomicLevel {
	if l == nil {
		return zap.NewAtomicLevel()
	}
	return l.level
}

// SetLevel changes log filtering at runtime. It is safe to call concurrently
// with logging.
func (l *Logger) SetLevel(level zapcore.Level) {
	if l == nil {
		return
	}
	l.level.SetLevel(level)
}

// Sync flushes buffered log entries. Sinks that cannot be synced (pipes and
// terminals report EINVAL/ENOTTY) are treated as already flushed so shutdown
// stays graceful when output is redirected.
func (l *Logger) Sync() error {
	if l == nil || l.Logger == nil {
		return nil
	}
	err := l.Logger.Sync()
	if err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) {
		return nil
	}
	return err
}

func parseLogLevel(level string) (zap.AtomicLevel, error) {
	if strings.TrimSpace(level) == "" {
		level = "info"
	}
	parsed, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return zap.AtomicLevel{}, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	return parsed, nil
}

func newLoggerCore(level zap.AtomicLevel, output io.Writer, bridge otellog.LoggerProvider) zapcore.Core {
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.Lock(zapcore.AddSync(output)), level)
	if bridge == nil {
		return core
	}
	otelCore := otelzap.NewCore(instrumentationScope, otelzap.WithLoggerProvider(bridge))
	return zapcore.NewTee(core, levelFilteredCore{Core: otelCore, level: level})
}

// levelFilteredCore applies the zap AtomicLevel to the otelzap bridge, which
// delegates filtering to its log provider and would otherwise bypass the level.
type levelFilteredCore struct {
	zapcore.Core
	level zapcore.LevelEnabler
}

func (c levelFilteredCore) Enabled(level zapcore.Level) bool {
	return c.level.Enabled(level) && c.Core.Enabled(level)
}

func (c levelFilteredCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if !c.level.Enabled(entry.Level) {
		return checked
	}
	return c.Core.Check(entry, checked)
}

func (c levelFilteredCore) With(fields []zapcore.Field) zapcore.Core {
	return levelFilteredCore{Core: c.Core.With(fields), level: c.level}
}
