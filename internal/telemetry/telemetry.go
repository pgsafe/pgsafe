package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Setup initialises OTel OTLP log export when OTEL_OTLP_ENDPOINT is set.
// It wraps base with a fan-out handler that writes to both the original
// destination and the OTLP endpoint, so existing JSON stdout logging is
// unaffected. Returns the (possibly wrapped) handler and a shutdown function
// that must be called before process exit to flush pending log records.
//
// Environment variables:
//
//	OTEL_OTLP_ENDPOINT     Base URL of the OTLP HTTP endpoint, e.g. http://collector:4318.
//	                       When unset OTel export is disabled and base is returned unchanged.
//	OTEL_OTLP_HEADERS      Optional comma-separated Key=Value pairs sent as HTTP headers,
//	                       e.g. Authorization=Bearer token,X-Dataset=prod.
//	OTEL_OTLP_SERVICE_NAME service.name resource attribute (default: pgsafe).
func Setup(ctx context.Context, base slog.Handler) (slog.Handler, func(), error) {
	endpoint := os.Getenv("OTEL_OTLP_ENDPOINT")
	if endpoint == "" {
		return base, func() {}, nil
	}

	serviceName := os.Getenv("OTEL_OTLP_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "pgsafe"
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("parse OTEL_OTLP_ENDPOINT %q: %w", endpoint, err)
	}

	opts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(u.Host),
	}
	if u.Scheme == "http" {
		opts = append(opts, otlploghttp.WithInsecure())
	}
	if raw := os.Getenv("OTEL_OTLP_HEADERS"); raw != "" {
		opts = append(opts, otlploghttp.WithHeaders(parseHeaders(raw)))
	}

	exp, err := otlploghttp.New(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("OTLP log exporter: %w", err)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	)

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	)

	otelHandler := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(provider))

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			slog.Error("OTel shutdown", "error", err)
		}
	}

	return multiHandler{base, otelHandler}, shutdown, nil
}

// parseHeaders parses "Key=Value,Key2=Value2" into a map.
func parseHeaders(raw string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			m[k] = v
		}
	}
	return m
}

// multiHandler fans slog records out to multiple handlers.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
