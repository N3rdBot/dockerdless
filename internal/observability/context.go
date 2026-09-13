package observability

import (
	"context"

	"go.uber.org/zap"
)

// contextKey scopes the observability values stored on a request context.
type contextKey int

const requestIDContextKey contextKey = iota

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
