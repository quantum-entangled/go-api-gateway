package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Tracing returns middleware that creates a server span for every incoming
// request. It extracts any incoming trace context from headers, starts a
// span, and annotates it with method, path, and status after the handler
// returns. Sets span status to codes.Error on 5xx responses.
func Tracing(tracer trace.Tracer, propagator propagation.TextMapPropagator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, "request", trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			span.SetAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.path", r.URL.Path),
				attribute.Int("http.status", rw.status),
			)

			if rw.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rw.status))
			}
		})
	}
}
