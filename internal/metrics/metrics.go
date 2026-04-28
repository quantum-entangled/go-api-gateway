package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds OpenTelemetry instruments for gateway-level HTTP instrumentation.
type Metrics struct {
	requestsTotal   metric.Int64Counter
	requestDuration metric.Float64Histogram
}

// NewMetrics creates a Metrics instance, registering all instruments on the
// given Meter. The Meter comes from a MeterProvider configured with an OTLP
// exporter (production) or a ManualReader (tests).
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	requestsTotal, err := meter.Int64Counter(
		"gateway.requests.total",
		metric.WithDescription("Total HTTP requests processed by the gateway."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	requestDuration, err := meter.Float64Histogram(
		"gateway.request.duration",
		metric.WithDescription("HTTP request duration in seconds."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
		),
	)
	if err != nil {
		return nil, err
	}

	return &Metrics{requestsTotal: requestsTotal, requestDuration: requestDuration}, nil
}

// Middleware returns chi-compatible middleware that records request count
// and duration for every HTTP request passing through.
func (m *Metrics) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &metricsWriter{ResponseWriter: w, status: http.StatusOK}

			start := time.Now()
			next.ServeHTTP(rw, r)
			duration := time.Since(start).Seconds()

			method := attribute.String("http.method", r.Method)
			routePattern := chi.RouteContext(r.Context()).RoutePattern()
			if routePattern == "" {
				routePattern = "(shed)"
			}
			path := attribute.String("http.path", routePattern)
			status := attribute.String("http.status", strconv.Itoa(rw.status))

			durationAttrs := attribute.NewSet(method, path)
			counterAttrs := attribute.NewSet(method, path, status)
			m.requestDuration.Record(r.Context(), duration, metric.WithAttributeSet(durationAttrs))
			m.requestsTotal.Add(r.Context(), 1, metric.WithAttributeSet(counterAttrs))
		})
	}
}

type metricsWriter struct {
	http.ResponseWriter
	status int
}

func (rw *metricsWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
