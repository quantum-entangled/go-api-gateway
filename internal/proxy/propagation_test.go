package proxy_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"go-api-gateway/internal/proxy"
)

func newTestTracing(t *testing.T) trace.Tracer {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })

	return provider.Tracer("test")
}

func TestInjectTraceContext_AddsTraceparentHeader(t *testing.T) {
	tracer := newTestTracing(t)
	ctx, span := tracer.Start(context.Background(), "test")
	defer span.End()

	originalPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(originalPropagator) })

	var header string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)
	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	h.ServeHTTP(rec, req)

	assert.NotEmpty(t, header)
}
