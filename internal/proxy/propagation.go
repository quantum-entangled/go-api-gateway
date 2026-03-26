package proxy

import (
	"net/http/httputil"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// injectTraceContext writes trace headers (traceparent, tracestate) into the
// outgoing request so upstream services can continue the trace.
func injectTraceContext(pr *httputil.ProxyRequest) {
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(pr.Out.Context(), propagation.HeaderCarrier(pr.Out.Header))
}
