package observability

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// RequestIDHeader is the HTTP header used to accept and echo request ids.
const RequestIDHeader = "X-Request-Id"

// Middleware returns HTTP middleware that assigns or echoes X-Request-Id,
// extracts inbound W3C trace context to start a server span, and emits one
// structured access log record carrying the request id, trace id, and span id.
func Middleware(logger *zap.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}

	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}

		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(
				request.Context(),
				propagation.HeaderCarrier(request.Header),
			)

			requestID := strings.TrimSpace(request.Header.Get(RequestIDHeader))
			if requestID == "" {
				requestID = newRequestID()
			}
			writer.Header().Set(RequestIDHeader, requestID)

			ctx = ContextWithRequestID(ctx, requestID)

			ctx, span := otel.Tracer(instrumentationScope).Start(ctx,
				request.Method+" "+request.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
			)
			defer span.End()

			recorder := &statusRecorder{ResponseWriter: writer}
			started := time.Now()

			next.ServeHTTP(recorder, request.WithContext(ctx))

			status := recorder.statusCode()
			span.SetAttributes(
				semconv.HTTPRequestMethodKey.String(request.Method),
				semconv.HTTPResponseStatusCodeKey.Int(status),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}

			spanContext := span.SpanContext()
			logger.Info("HTTP request",
				zap.String("request_id", requestID),
				zap.String("method", request.Method),
				zap.String("path", request.URL.Path),
				zap.Int("status", status),
				zap.Int("response_bytes", recorder.bytesWritten),
				zap.Duration("duration", time.Since(started)),
				zap.String("trace_id", spanContext.TraceID().String()),
				zap.String("span_id", spanContext.SpanID().String()),
				ContextField(ctx),
			)
		})
	}
}

// statusRecorder captures the response status and body size while delegating
// the response itself to the wrapped writer.
type statusRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(body)
	r.bytesWritten += written
	return written, err
}

// Unwrap lets http.ResponseController reach the wrapped writer's optional
// interfaces (flush, hijack).
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func newRequestID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err == nil {
		return hex.EncodeToString(buffer[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 16)
}
