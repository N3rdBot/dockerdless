package observability

import (
	"context"

	"go.uber.org/zap"
)

// contextKey scopes the observability values stored on a request context.
type contextKey int

const (
	loggerContextKey contextKey = iota
	requestIDContextKey
)

// ContextWithLogger attaches logger to ctx so handlers can log with the
// request-scoped fields (request id, correlation context) already applied.
func ContextWithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return context.WithValue(ctx, loggerContextKey, logger)
}

// LoggerFromContext returns the logger attached to ctx. It always returns a
// usable logger so callers never need a nil check.
func LoggerFromContext(ctx context.Context) *zap.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerContextKey).(*zap.Logger); ok && logger != nil {
			return logger
		}
	}
	return zap.NewNop()
}

// ContextWithRequestID attaches requestID to ctx.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDContextKey, requestID)
}

// RequestIDFromContext returns the request id attached to ctx, or an empty
// string when the context carries none.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if requestID, ok := ctx.Value(requestIDContextKey).(string); ok {
		return requestID
	}
	return ""
}

// ContextField returns the zap field that hands ctx to the otelzap bridge. The
// bridge treats a context field as the emit context, so the exported record
// carries the active span's trace and span ids.
func ContextField(ctx context.Context) zap.Field {
	// zap.Any on a context value preserves the dynamic type, which the otelzap
	// bridge detects via an interface assertion on the field's Interface value.
	return zap.Any(contextFieldKey, ctx)
}

const contextFieldKey = "context"
