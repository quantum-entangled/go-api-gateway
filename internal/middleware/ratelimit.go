package middleware

import (
	"encoding/json"
	"net"
	"net/http"

	"go-api-gateway/internal/ratelimit"
)

// RateLimit rejects requests with 429 when the per-key rate limit is exceeded.
// The key is derived from the request by the keyFunc parameter.
func RateLimit(limiter ratelimit.Limiter, keyFunc func(r *http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFunc(r)
			allowed := limiter.Allow(key)
			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// KeyByIP returns the client's IP address from RemoteAddr, stripping the port.
func KeyByIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
