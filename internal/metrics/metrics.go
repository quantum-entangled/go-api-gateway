package metrics

import (
	"net/http"
	"strconv"
	"time"

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
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			start := time.Now()
			next.ServeHTTP(rw, r)
			duration := time.Since(start).Seconds()

			method := attribute.String("http.method", r.Method)
			path := attribute.String("http.path", r.URL.Path)
			status := attribute.String("http.status", strconv.Itoa(rw.status))

			m.requestDuration.Record(r.Context(), duration,
				metric.WithAttributeSet(attribute.NewSet(method, path)))
			m.requestsTotal.Add(r.Context(), 1,
				metric.WithAttributeSet(attribute.NewSet(method, path, status)))
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
