package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"

	"go-api-gateway/internal/circuitbreaker"
)

// LoadBalancer picks the next healthy upstream URL.
// Defined at the consumer - same pattern as HealthChecker in loadbalancer.
type LoadBalancer interface {
	Next() (string, error)
}

type targetKeyType string

const targetKey targetKeyType = "proxy-target"

func newTargetContext(ctx context.Context, target string) context.Context {
	return context.WithValue(ctx, targetKey, target)
}

// TargetFromContext extracts the upstream URL stored by ServeHTTP.
func TargetFromContext(ctx context.Context) (string, bool) {
	target, ok := ctx.Value(targetKey).(string)
	return target, ok
}

// Handler picks an upstream via load balancer, checks the circuit breaker,
// and reverse-proxies the request. It is the main HTTP handler for proxied routes.
type Handler struct {
	lb       LoadBalancer
	breakers map[string]*circuitbreaker.Breaker
	proxy    *httputil.ReverseProxy
	logger   *slog.Logger
}

// NewHandler creates a Handler that load-balances across upstreams with
// circuit breaker protection. Each upstream URL must have a corresponding
// entry in the breakers map.
//
// The returned Handler owns a single shared ReverseProxy whose Rewrite
// function reads the target URL from the request context (set by ServeHTTP).
func NewHandler(lb LoadBalancer, breakers map[string]*circuitbreaker.Breaker, logger *slog.Logger) *Handler {
	h := &Handler{
		lb:       lb,
		breakers: breakers,
		logger:   logger,
	}

	h.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := mustGetTargetFromContext(pr.Out.Context())
			targetURL := mustParseURL(target)

			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			pr.Out.Host = targetURL.Host

			pr.SetXForwarded()
			injectTraceContext(pr)
		},
		ModifyResponse: func(resp *http.Response) error {
			target := mustGetTargetFromContext(resp.Request.Context())
			breaker := h.mustGetBreaker(target)

			if resp.StatusCode >= 500 {
				breaker.RecordFailure()
			} else {
				breaker.RecordSuccess()
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			target := mustGetTargetFromContext(r.Context())
			breaker := h.mustGetBreaker(target)

			breaker.RecordFailure()
			h.logger.Error("proxy", "error", err)

			jsonError(w, http.StatusBadGateway, map[string]string{
				"error": "upstream unavailable",
			})
		},
	}

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upstream, err := h.lb.Next()
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no healthy upstreams",
		})
		return
	}

	breaker := h.mustGetBreaker(upstream)
	if err := breaker.Allow(); err != nil {
		jsonError(w, http.StatusServiceUnavailable, map[string]string{
			"error":    "circuit breaker is open",
			"upstream": upstream,
		})
		return
	}

	ctx := newTargetContext(r.Context(), upstream)
	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) mustGetBreaker(upstream string) *circuitbreaker.Breaker {
	breaker, ok := h.breakers[upstream]
	if !ok {
		panic("proxy: circuit breaker not found")
	}
	return breaker
}

func mustGetTargetFromContext(ctx context.Context) string {
	target, ok := TargetFromContext(ctx)
	if !ok {
		panic("proxy: target URL not found in request context")
	}
	return target
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic("proxy: invalid upstream URL: " + raw)
	}
	return u
}

func jsonError(w http.ResponseWriter, status int, msg map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(msg)
}
