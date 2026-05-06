package middleware

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"

	"go-api-gateway/internal/ratelimit"
)

// RateLimit rejects requests with 429 when the per-key rate limit is
// exceeded. Fails open on limiter error, as a broken limiter must not take
// down the request path.
func RateLimit(
	limiter ratelimit.Limiter,
	keyFunc func(r *http.Request) string,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFunc(r)
			allowed, err := limiter.Allow(r.Context(), key)
			if err != nil {
				logger.Error("rate limiter error, failing open", "key", key, "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
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

// KeyByJWTSub returns the authenticated user's subject from context,
// falling back to KeyByIP when no claims are present. Must be mounted
// after JWTAuth in the chain, otherwise it always falls back.
func KeyByJWTSub(r *http.Request) string {
	if claims, ok := ClaimsFromContext(r.Context()); ok && claims.Subject != "" {
		return claims.Subject
	}
	return KeyByIP(r)
}
