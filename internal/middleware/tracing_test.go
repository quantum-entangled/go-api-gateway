package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go-api-gateway/internal/middleware"
)

func newTestTracing(t *testing.T) (*tracetest.InMemoryExporter, func(http.Handler) http.Handler) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })

	tracer := provider.Tracer("test")
	propagator := propagation.TraceContext{}

	return exporter, middleware.Tracing(tracer, propagator)
}

func spanAttrMap(attrs []attribute.KeyValue) map[string]any {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		switch a.Value.Type() {
		case attribute.STRING:
			m[string(a.Key)] = a.Value.AsString()
		case attribute.INT64:
			m[string(a.Key)] = a.Value.AsInt64()
		case attribute.BOOL:
			m[string(a.Key)] = a.Value.AsBool()
		case attribute.FLOAT64:
			m[string(a.Key)] = a.Value.AsFloat64()
		}
	}
	return m
}

func TestTracing_CreatesSpan(t *testing.T) {
	exporter, mw := newTestTracing(t)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "request", spans[0].Name)

	attrs := spanAttrMap(spans[0].Attributes)
	assert.Equal(t, "GET", attrs["http.method"])
	assert.Equal(t, "/test", attrs["http.path"])
	assert.Equal(t, int64(200), attrs["http.status"])
}

func TestTracing_SetsErrorStatusOn5xx(t *testing.T) {
	exporter, mw := newTestTracing(t)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	span := exporter.GetSpans()[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "Service Unavailable", span.Status.Description)

	attrs := spanAttrMap(span.Attributes)
	assert.Equal(t, int64(503), attrs["http.status"])
}

func TestTracing_OkStatusOn2xx(t *testing.T) {
	exporter, mw := newTestTracing(t)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	span := exporter.GetSpans()[0]
	assert.Equal(t, codes.Unset, span.Status.Code)
	assert.Empty(t, span.Status.Description)
}

func TestTracing_PropagatesContext(t *testing.T) {
	exporter, mw := newTestTracing(t)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	traceID := "11111111111111111111111111111111"
	spanID := "2222222222222222"
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Traceparent", "00-"+traceID+"-"+spanID+"-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	span := exporter.GetSpans()[0]
	assert.Equal(t, traceID, span.Parent.TraceID().String())
	assert.Equal(t, spanID, span.Parent.SpanID().String())
}
