package observability

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLoggerLevelChangesWithoutRebuild(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewLogger("info", WithLogOutput(&output))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	logger.Debug("dropped-debug-record")
	if strings.Contains(output.String(), "dropped-debug-record") {
		t.Fatalf("debug record was emitted at info level: %q", output.String())
	}

	logger.Level().SetLevel(zap.DebugLevel)
	if got := logger.Level().Level(); got != zap.DebugLevel {
		t.Fatalf("atomic level = %s, want debug", got)
	}

	logger.Debug("emitted-debug-record")
	if !strings.Contains(output.String(), "emitted-debug-record") {
		t.Fatalf("debug record missing after SetLevel(Debug): %q", output.String())
	}

	logger.SetLevel(zap.InfoLevel)
	logger.Debug("filtered-again-record")
	if strings.Contains(output.String(), "filtered-again-record") {
		t.Fatalf("debug record was emitted after raising level back to info: %q", output.String())
	}
}

func TestNewLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := NewLogger("not-a-level"); err == nil {
		t.Fatal("NewLogger accepted an invalid level")
	}
}

func TestBootstrapOfflineStartsAndShutsDownIdempotently(t *testing.T) {
	runtime, err := Bootstrap(BootstrapConfig{
		ServiceName: "dockerdless-offline-test",
		LogLevel:    "info",
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Shutdown(context.Background())
	})

	if runtime.Logger() == nil {
		t.Fatal("logger is nil")
	}
	if runtime.Telemetry().TracerProvider() == nil {
		t.Fatal("tracer provider is nil")
	}
	if runtime.Telemetry().MeterProvider() == nil {
		t.Fatal("meter provider is nil")
	}
	if runtime.Telemetry().LoggerProvider() != nil {
		t.Fatal("log provider must be absent without an endpoint")
	}
	if runtime.Telemetry().OTLPEnabled() {
		t.Fatal("OTLP must stay disabled without an endpoint")
	}

	runtime.Logger().Info("offline startup complete")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestTelemetryShutdownIsIdempotentUnderConcurrency(t *testing.T) {
	telemetry, err := BootstrapTelemetry("dockerdless-shutdown-test")
	if err != nil {
		t.Fatalf("BootstrapTelemetry: %v", err)
	}

	const callers = 8
	var waitGroup sync.WaitGroup
	errs := make([]error, callers)
	for index := range errs {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			errs[index] = telemetry.Shutdown(context.Background())
		}(index)
	}
	waitGroup.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("shutdown caller %d: %v", index, err)
		}
	}
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("sequential shutdown after concurrent callers: %v", err)
	}
}

func TestBootstrapWithUnreachableCollectorDoesNotBlockStartup(t *testing.T) {
	started := time.Now()
	runtime, err := Bootstrap(BootstrapConfig{
		ServiceName:  "dockerdless-unreachable-test",
		LogLevel:     "info",
		OTLPEndpoint: "127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Bootstrap blocked on the unreachable collector for %s", elapsed)
	}
	if !runtime.Telemetry().OTLPEnabled() {
		t.Fatal("OTLP exporters were not wired for a configured endpoint")
	}

	t.Cleanup(func() {
		_ = runtime.Shutdown(context.Background())
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runtime.Shutdown(shutdownCtx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not return for an unreachable collector")
	}
}

func TestBootstrapRejectsEndpointWithoutHost(t *testing.T) {
	if _, err := Bootstrap(BootstrapConfig{
		ServiceName:  "dockerdless-invalid-endpoint-test",
		OTLPEndpoint: "ftp://collector:4317",
	}); err == nil {
		t.Fatal("Bootstrap accepted an unsupported endpoint scheme")
	}
}

func TestLoggerLevelChangesAreRaceFree(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewLogger("info", WithLogOutput(&output))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	var writers sync.WaitGroup
	for worker := range 4 {
		writers.Go(func() {
			for range 50 {
				logger.Debug("debug record", zap.Int("worker", worker))
				logger.Info("info record", zap.Int("worker", worker))
			}
		})
	}

	var toggler sync.WaitGroup
	toggler.Go(func() {
		for range 100 {
			logger.SetLevel(zapcore.DebugLevel)
			logger.SetLevel(zapcore.InfoLevel)
		}
	})

	writers.Wait()
	toggler.Wait()

	if output.Len() == 0 {
		t.Fatal("expected log output from concurrent writers")
	}
}
