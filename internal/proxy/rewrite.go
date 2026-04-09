package proxy

import (
	"net/http/httputil"
	"strings"

	"go-api-gateway/internal/middleware"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// injectTraceContext writes trace headers (traceparent, tracestate) into the
// outgoing request so upstream services can continue the trace.
func injectTraceContext(pr *httputil.ProxyRequest) {
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(pr.Out.Context(), propagation.HeaderCarrier(pr.Out.Header))
}

// injectUserHeaders forwards authenticated user identity to upstreams.
// If JWTAuth middleware has stored claims in the context, this sets
// X-User (subject) and X-Roles (comma-separated) headers on the
// outgoing request. Does nothing for unauthenticated requests.
func injectUserHeaders(pr *httputil.ProxyRequest) {
	claims, ok := middleware.ClaimsFromContext(pr.Out.Context())
	if !ok {
		return
	}
	pr.Out.Header.Set("X-User", claims.Subject)
	pr.Out.Header.Set("X-Roles", strings.Join(claims.Roles, ","))
}
