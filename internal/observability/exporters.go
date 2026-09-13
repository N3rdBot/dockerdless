package observability

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

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
