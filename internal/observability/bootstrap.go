// Package observability provides logging and telemetry bootstrap helpers.
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
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

// Telemetry owns the process-wide OpenTelemetry providers.
type Telemetry struct {
	serviceName    string
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider
	otlpEnabled    bool

	loggerMu sync.Mutex
	logger   *Logger

	shutdownOnce sync.Once
	shutdownErr  error
}

// TelemetryOption configures BootstrapTelemetry.
type TelemetryOption func(*telemetryConfig)

type telemetryConfig struct {
	endpoint string
}

// WithOTLPEndpoint enables OTLP export to endpoint. The endpoint accepts either
// a gRPC host:port pair (plaintext) or an http/https URL; https enables client
// transport security. An empty endpoint leaves the daemon fully offline.
func WithOTLPEndpoint(endpoint string) TelemetryOption {
	return func(cfg *telemetryConfig) {
		cfg.endpoint = endpoint
	}
}

// BootstrapTelemetry installs the process-wide tracer, meter, and (when an OTLP
// endpoint is configured) logger providers with a service.name resource.
// Exporters are only constructed when an endpoint is configured, so an empty
// endpoint performs no network calls and startup never blocks on a collector.
func BootstrapTelemetry(serviceName string, opts ...TelemetryOption) (*Telemetry, error) {
	if strings.TrimSpace(serviceName) == "" {
		return nil, errors.New("OpenTelemetry service name must not be empty")
	}

	cfg := telemetryConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	target, secure, err := parseOTLPEndpoint(cfg.endpoint)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("build OpenTelemetry resource: %w", err)
	}

	tracerOptions := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	meterOptions := []sdkmetric.Option{sdkmetric.WithResource(res)}
	loggerOptions := make([]sdklog.LoggerProviderOption, 0, 1)

	if target != "" {
		traceExporter, err := newTraceExporter(target, secure)
		if err != nil {
			return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
		}
		tracerOptions = append(tracerOptions, sdktrace.WithBatcher(traceExporter))

		metricExporter, err := newMetricExporter(target, secure)
		if err != nil {
			return nil, fmt.Errorf("initialize OTLP metric exporter: %w", err)
		}
		meterOptions = append(meterOptions, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))

		logExporter, err := newLogExporter(target, secure)
		if err != nil {
			return nil, fmt.Errorf("initialize OTLP log exporter: %w", err)
		}
		loggerOptions = append(loggerOptions, sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)))
	}

	tracerProvider := sdktrace.NewTracerProvider(tracerOptions...)
	meterProvider := sdkmetric.NewMeterProvider(meterOptions...)

	var loggerProvider *sdklog.LoggerProvider
	if target != "" {
		loggerProvider = sdklog.NewLoggerProvider(loggerOptions...)
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	if loggerProvider != nil {
		logglobal.SetLoggerProvider(loggerProvider)
	}

	return &Telemetry{
		serviceName:    serviceName,
		tracerProvider: tracerProvider,
		meterProvider:  meterProvider,
		loggerProvider: loggerProvider,
		otlpEnabled:    target != "",
	}, nil
}

// ServiceName returns the service name used for telemetry.
func (t *Telemetry) ServiceName() string {
	if t == nil {
		return ""
	}
	return t.serviceName
}

// OTLPEnabled reports whether OTLP exporters were wired during bootstrap.
func (t *Telemetry) OTLPEnabled() bool {
	if t == nil {
		return false
	}
	return t.otlpEnabled
}

// TracerProvider returns the installed tracer provider.
func (t *Telemetry) TracerProvider() *sdktrace.TracerProvider {
	if t == nil {
		return nil
	}
	return t.tracerProvider
}

// MeterProvider returns the installed meter provider.
func (t *Telemetry) MeterProvider() *sdkmetric.MeterProvider {
	if t == nil {
		return nil
	}
	return t.meterProvider
}

// LoggerProvider returns the OTLP log provider, or nil when OTLP is disabled.
func (t *Telemetry) LoggerProvider() otellog.LoggerProvider {
	if t == nil || t.loggerProvider == nil {
		return nil
	}
	return t.loggerProvider
}

// AttachLogger lets Shutdown flush the daemon logger. It is safe to call before
// any Shutdown.
func (t *Telemetry) AttachLogger(logger *Logger) {
	if t == nil {
		return
	}
	t.loggerMu.Lock()
	defer t.loggerMu.Unlock()
	t.logger = logger
}

func (t *Telemetry) attachedLogger() *Logger {
	t.loggerMu.Lock()
	defer t.loggerMu.Unlock()
	return t.logger
}

// Shutdown flushes and closes every provider installed by BootstrapTelemetry
// and flushes the attached zap logger. It is idempotent and safe to call
// concurrently: the first outcome is returned to all callers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.shutdownOnce.Do(func() {
		var errs []error
		if t.tracerProvider != nil {
			errs = append(errs,
				t.tracerProvider.ForceFlush(ctx),
				t.tracerProvider.Shutdown(ctx),
			)
		}
		if t.meterProvider != nil {
			errs = append(errs,
				t.meterProvider.ForceFlush(ctx),
				t.meterProvider.Shutdown(ctx),
			)
		}
		if t.loggerProvider != nil {
			errs = append(errs,
				t.loggerProvider.ForceFlush(ctx),
				t.loggerProvider.Shutdown(ctx),
			)
		}
		if logger := t.attachedLogger(); logger != nil {
			errs = append(errs, logger.Sync())
		}
		t.shutdownErr = errors.Join(errs...)
	})
	return t.shutdownErr
}

// BootstrapConfig is the typed observability configuration assembled from the
// daemon configuration.
type BootstrapConfig struct {
	ServiceName  string
	LogLevel     string
	OTLPEndpoint string
	LogOutput    io.Writer
}

// Runtime couples the daemon logger with its telemetry providers.
type Runtime struct {
	logger    *Logger
	telemetry *Telemetry
}

// Bootstrap builds the correlated logging and telemetry pipeline.
func Bootstrap(cfg BootstrapConfig) (*Runtime, error) {
	telemetry, err := BootstrapTelemetry(cfg.ServiceName, WithOTLPEndpoint(cfg.OTLPEndpoint))
	if err != nil {
		return nil, fmt.Errorf("initialize telemetry: %w", err)
	}

	loggerOptions := make([]LoggerOption, 0, 2)
	if cfg.LogOutput != nil {
		loggerOptions = append(loggerOptions, WithLogOutput(cfg.LogOutput))
	}
	if provider := telemetry.LoggerProvider(); provider != nil {
		loggerOptions = append(loggerOptions, WithLogBridge(provider))
	}

	logger, err := NewLogger(cfg.LogLevel, loggerOptions...)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("initialize logging: %w", err),
			telemetry.Shutdown(context.Background()),
		)
	}
	telemetry.AttachLogger(logger)

	return &Runtime{logger: logger, telemetry: telemetry}, nil
}

// Logger returns the daemon logger.
func (r *Runtime) Logger() *Logger {
	if r == nil {
		return nil
	}
	return r.logger
}

// Telemetry returns the process-wide telemetry providers.
func (r *Runtime) Telemetry() *Telemetry {
	if r == nil {
		return nil
	}
	return r.telemetry
}

// Shutdown gracefully flushes logging and telemetry. It is idempotent.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.telemetry.Shutdown(ctx)
}

// parseOTLPEndpoint normalizes the configured endpoint into a gRPC target and
// whether client transport security is required. An empty endpoint disables
// OTLP export entirely.
func parseOTLPEndpoint(endpoint string) (target string, secure bool, err error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return "", false, nil
	}
	if !strings.Contains(trimmed, "://") {
		return trimmed, false, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false, fmt.Errorf("invalid OpenTelemetry endpoint %q: %w", endpoint, err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", false, fmt.Errorf("invalid OpenTelemetry endpoint scheme %q: use http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", false, fmt.Errorf("invalid OpenTelemetry endpoint %q: missing host", endpoint)
	}
	return parsed.Host, parsed.Scheme == "https", nil
}

func newTraceExporter(target string, secure bool) (sdktrace.SpanExporter, error) {
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(target)}
	if !secure {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	return otlptracegrpc.New(context.Background(), options...)
}

func newMetricExporter(target string, secure bool) (sdkmetric.Exporter, error) {
	options := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(target)}
	if !secure {
		options = append(options, otlpmetricgrpc.WithInsecure())
	}
	return otlpmetricgrpc.New(context.Background(), options...)
}

func newLogExporter(target string, secure bool) (sdklog.Exporter, error) {
	options := []otlploggrpc.Option{otlploggrpc.WithEndpoint(target)}
	if !secure {
		options = append(options, otlploggrpc.WithInsecure())
	}
	return otlploggrpc.New(context.Background(), options...)
}
