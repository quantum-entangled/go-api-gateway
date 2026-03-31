package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go-api-gateway/internal/metrics"
)

func newTestMetrics(t *testing.T) (*metrics.Metrics, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })

	meter := provider.Meter("test")
	m, err := metrics.NewMetrics(meter)
	require.NoError(t, err)

	return m, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	return rm
}

func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}

	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func assertAttr(t *testing.T, attrs attribute.Set, key, expected string) {
	t.Helper()

	val, ok := attrs.Value(attribute.Key(key))
	require.True(t, ok, "attribute %q not found", key)
	assert.Equal(t, expected, val.AsString(), "attribute %q", key)
}

func TestMiddlewareRecordsRequestsTotal(t *testing.T) {
	m, reader := newTestMetrics(t)

	r := chi.NewRouter()
	r.Use(m.Middleware())
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	rm := collect(t, reader)
	got := findMetric(t, rm, "gateway.requests.total")

	sum, ok := got.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data type")
	require.Len(t, sum.DataPoints, 1)

	dp := sum.DataPoints[0]
	assert.Equal(t, int64(1), dp.Value)

	assertAttr(t, dp.Attributes, "http.method", "GET")
	assertAttr(t, dp.Attributes, "http.path", "/test")
	assertAttr(t, dp.Attributes, "http.status", "200")
}

func TestMiddlewareRecordsDuration(t *testing.T) {
	m, reader := newTestMetrics(t)

	r := chi.NewRouter()
	r.Use(m.Middleware())
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	rm := collect(t, reader)
	got := findMetric(t, rm, "gateway.request.duration")

	hist, ok := got.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64] data type")
	require.Len(t, hist.DataPoints, 1)

	dp := hist.DataPoints[0]
	assert.Equal(t, uint64(1), dp.Count)

	assertAttr(t, dp.Attributes, "http.method", "GET")
	assertAttr(t, dp.Attributes, "http.path", "/test")
}

func TestMiddlewareDefaultsTo200WithoutWriteHeader(t *testing.T) {
	m, reader := newTestMetrics(t)

	r := chi.NewRouter()
	r.Use(m.Middleware())
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	rm := collect(t, reader)
	got := findMetric(t, rm, "gateway.requests.total")

	sum, _ := got.Data.(metricdata.Sum[int64])
	dp := sum.DataPoints[0]

	assertAttr(t, dp.Attributes, "http.status", "200")
}

func TestMiddlewareRecords5xxStatus(t *testing.T) {
	m, reader := newTestMetrics(t)

	r := chi.NewRouter()
	r.Use(m.Middleware())
	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	rm := collect(t, reader)
	got := findMetric(t, rm, "gateway.requests.total")

	sum, _ := got.Data.(metricdata.Sum[int64])
	dp := sum.DataPoints[0]

	assertAttr(t, dp.Attributes, "http.status", "502")
}
