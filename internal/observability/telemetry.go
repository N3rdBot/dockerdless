package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

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
