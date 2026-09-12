package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func lastLogEntry(t *testing.T, output []byte) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatalf("no log output to decode: %q", output)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("decode log entry: %v (raw: %q)", err, lines[len(lines)-1])
	}
	return entry
}

func installTestTelemetry(t *testing.T) {
	t.Helper()

	tracerProvider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
}

func TestMiddlewareCorrelatesAccessLogWithRequestIDAndSpan(t *testing.T) {
	exporter := &recordingLogExporter{}
	logProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() {
		if err := logProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown log provider: %v", err)
		}
	})

	var output bytes.Buffer
	logger, err := NewLogger("info", WithLogOutput(&output), WithLogBridge(logProvider))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	installTestTelemetry(t)

	var (
		handlerRequestID string
		handlerSpan      trace.SpanContext
	)
	handler := Middleware(logger.Logger)(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handlerRequestID = RequestIDFromContext(request.Context())
		handlerSpan = trace.SpanFromContext(request.Context()).SpanContext()
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1.44/containers/create", strings.NewReader("{}"))
	request.Header.Set(RequestIDHeader, "client-supplied-id")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get(RequestIDHeader); got != "client-supplied-id" {
		t.Fatalf("response %s = %q, want %q", RequestIDHeader, got, "client-supplied-id")
	}
	if handlerRequestID != "client-supplied-id" {
		t.Fatalf("context request id = %q, want client-supplied-id", handlerRequestID)
	}
	if !handlerSpan.IsValid() {
		t.Fatal("handler observed an invalid span context")
	}

	entry := lastLogEntry(t, output.Bytes())
	if got := entry["request_id"]; got != "client-supplied-id" {
		t.Fatalf("access log request_id = %v, want client-supplied-id", got)
	}
	if got := entry["trace_id"]; got != handlerSpan.TraceID().String() {
		t.Fatalf("access log trace_id = %v, want %s", got, handlerSpan.TraceID())
	}
	if got := entry["span_id"]; got != handlerSpan.SpanID().String() {
		t.Fatalf("access log span_id = %v, want %s", got, handlerSpan.SpanID())
	}
	if got := entry["status"]; got != float64(http.StatusCreated) {
		t.Fatalf("access log status = %v, want %d", got, http.StatusCreated)
	}

	records := exporter.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 bridged access log record, got %d", len(records))
	}
	if got := records[0].TraceID(); got != handlerSpan.TraceID() {
		t.Fatalf("bridged record trace_id = %s, want %s", got, handlerSpan.TraceID())
	}
	if got := records[0].SpanID(); got != handlerSpan.SpanID() {
		t.Fatalf("bridged record span_id = %s, want %s", got, handlerSpan.SpanID())
	}
	if got := records[0].Body().AsString(); got != "HTTP request" {
		t.Fatalf("bridged record body = %q, want %q", got, "HTTP request")
	}
}

func TestMiddlewareGeneratesRequestIDWhenHeaderMissing(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewLogger("info", WithLogOutput(&output))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	installTestTelemetry(t)

	var handlerRequestID string
	handler := Middleware(logger.Logger)(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		handlerRequestID = RequestIDFromContext(request.Context())
	}))

	request := httptest.NewRequest(http.MethodGet, "/_ping", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	generated := response.Header().Get(RequestIDHeader)
	if generated == "" {
		t.Fatalf("response %s was not generated", RequestIDHeader)
	}
	if len(generated) != 32 {
		t.Fatalf("generated request id %q has length %d, want 32", generated, len(generated))
	}
	if handlerRequestID != generated {
		t.Fatalf("context request id = %q, want %q", handlerRequestID, generated)
	}
	if got := lastLogEntry(t, output.Bytes())["request_id"]; got != generated {
		t.Fatalf("access log request_id = %v, want %s", got, generated)
	}
}

func TestMiddlewareContinuesInboundTraceContext(t *testing.T) {
	installTestTelemetry(t)
	tracerProvider := otel.GetTracerProvider()

	var serverSpan trace.SpanContext
	handler := Middleware(zap.NewNop())(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		serverSpan = trace.SpanFromContext(request.Context()).SpanContext()
	}))

	upstreamCtx, upstreamSpan := tracerProvider.Tracer("upstream").Start(context.Background(), "upstream")
	upstreamSpanContext := upstreamSpan.SpanContext()
	carrier := propagation.HeaderCarrier{}
	otel.GetTextMapPropagator().Inject(upstreamCtx, carrier)

	request := httptest.NewRequest(http.MethodGet, "/version", nil)
	request.Header = http.Header(carrier)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	upstreamSpan.End()

	if !serverSpan.IsValid() {
		t.Fatal("server span context is invalid")
	}
	if serverSpan.TraceID() != upstreamSpanContext.TraceID() {
		t.Fatalf("server trace_id = %s, want inbound %s", serverSpan.TraceID(), upstreamSpanContext.TraceID())
	}
	if serverSpan.SpanID() == upstreamSpanContext.SpanID() {
		t.Fatal("server span reused the inbound span id instead of starting a child span")
	}
	if serverSpan.IsRemote() {
		t.Fatal("server span must be local, not remote")
	}
}
