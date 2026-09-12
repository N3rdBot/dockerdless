// Package observability provides logging and tracing bootstrap helpers.
package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// NewLogger builds the daemon's structured logger.
func NewLogger(level string) (*zap.Logger, error) {
	if strings.TrimSpace(level) == "" {
		level = "info"
	}

	parsedLevel, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}

	loggerConfig := zap.NewProductionConfig()
	loggerConfig.Level = parsedLevel
	logger, err := loggerConfig.Build()
	if err != nil {
		return nil, err
	}
	return logger, nil
}

// Telemetry owns the process-wide OpenTelemetry tracer provider.
type Telemetry struct {
	provider    *sdktrace.TracerProvider
	serviceName string
}

// BootstrapTelemetry installs a no-exporter tracer provider for the daemon
// scaffold. Exporter wiring will be added without changing callers.
func BootstrapTelemetry(serviceName string) (*Telemetry, error) {
	if strings.TrimSpace(serviceName) == "" {
		return nil, errors.New("OpenTelemetry service name must not be empty")
	}

	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	return &Telemetry{provider: provider, serviceName: serviceName}, nil
}

// ServiceName returns the service name used for telemetry.
func (t *Telemetry) ServiceName() string {
	if t == nil {
		return ""
	}
	return t.serviceName
}

// Shutdown flushes and closes the tracer provider.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	return t.provider.Shutdown(ctx)
}
