package observability

import (
	"context"
	"io"
	"sync"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// recordingLogExporter captures OpenTelemetry log records emitted through the
// otelzap bridge so tests can assert on correlation fields.
type recordingLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

// Export implements sdklog.Exporter.
func (e *recordingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

// Shutdown implements sdklog.Exporter.
func (e *recordingLogExporter) Shutdown(context.Context) error { return nil }

// ForceFlush implements sdklog.Exporter.
func (e *recordingLogExporter) ForceFlush(context.Context) error { return nil }

func (e *recordingLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

// TestLogRecordsCarryActiveSpanIdentity is the failing-first proof for log/trace
// correlation: a zap record emitted with a request context must reach the
// OpenTelemetry log pipeline carrying the active span's trace and span IDs.
func TestLogRecordsCarryActiveSpanIdentity(t *testing.T) {
	exporter := &recordingLogExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown log provider: %v", err)
		}
	})

	logger, err := NewLogger("info", WithLogOutput(io.Discard), WithLogBridge(provider))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	tracerProvider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
	tracer := tracerProvider.Tracer("correlation-test")

	ctx, span := tracer.Start(context.Background(), "correlated-operation")
	logger.Info("correlated record", ContextField(ctx))
	span.End()

	records := exporter.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 exported log record, got %d", len(records))
	}

	wantTraceID := span.SpanContext().TraceID()
	wantSpanID := span.SpanContext().SpanID()
	if !wantTraceID.IsValid() || !wantSpanID.IsValid() {
		t.Fatalf("test span context is invalid: trace=%s span=%s", wantTraceID, wantSpanID)
	}

	if got := records[0].TraceID(); got != wantTraceID {
		t.Fatalf("record trace_id = %s, want %s", got, wantTraceID)
	}
	if got := records[0].SpanID(); got != wantSpanID {
		t.Fatalf("record span_id = %s, want %s", got, wantSpanID)
	}
	if got := records[0].TraceID().String(); len(got) != 32 {
		t.Fatalf("record trace_id hex length = %d (%q), want 32", len(got), got)
	}
	if got := records[0].SpanID().String(); len(got) != 16 {
		t.Fatalf("record span_id hex length = %d (%q), want 16", len(got), got)
	}
}
