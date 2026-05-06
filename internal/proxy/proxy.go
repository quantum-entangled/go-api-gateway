package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

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

// TransportConfig controls the HTTP transport used for proxying to upstreams.
type TransportConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	DialTimeout         time.Duration
	TLSHandshakeTimeout time.Duration
}

// NewHandler creates a Handler that load-balances across upstreams with
// circuit breaker protection and a tuned HTTP transport for upstream connections.
// Each upstream URL must have a corresponding entry in the breakers map.
// The returned Handler owns a single shared ReverseProxy whose Rewrite
// function reads the target URL from the request context (set by ServeHTTP).
func NewHandler(lb LoadBalancer, breakers map[string]*circuitbreaker.Breaker, tc TransportConfig, logger *slog.Logger) *Handler {
	h := &Handler{
		lb:       lb,
		breakers: breakers,
		logger:   logger,
	}

	h.proxy = &httputil.ReverseProxy{
		Transport: newUpstreamTransport(tc),
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := mustGetTargetFromContext(pr.Out.Context())
			targetURL := mustParseURL(target)

			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			pr.Out.Host = targetURL.Host

			pr.SetXForwarded()
			injectTraceContext(pr)
			injectUserHeaders(pr)
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
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				jsonError(w, http.StatusRequestEntityTooLarge, map[string]string{
					"error": "request body too large",
				})
				return
			}

			target := mustGetTargetFromContext(r.Context())
			breaker := h.mustGetBreaker(target)

			breaker.RecordFailure()
			h.logger.Error("proxy", "error", err)

			status, msg := classifyProxyError(err)
			jsonError(w, status, map[string]string{"error": msg})
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

func newUpstreamTransport(tc TransportConfig) *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   tc.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          tc.MaxIdleConns,
		MaxIdleConnsPerHost:   tc.MaxIdleConnsPerHost,
		IdleConnTimeout:       tc.IdleConnTimeout,
		TLSHandshakeTimeout:   tc.TLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

func classifyProxyError(err error) (int, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "upstream timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout, "upstream timeout"
	}
	return http.StatusBadGateway, "upstream unavailable"
}

func jsonError(w http.ResponseWriter, status int, msg map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(msg)
}
