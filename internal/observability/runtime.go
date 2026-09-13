package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
)

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
