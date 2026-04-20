package middleware

import (
	"encoding/json"
	"net/http"
)

// ConcurrencyLimit sheds requests with 503 once the in-flight count reaches
// limit, to keep goroutines from piling up during overload. The semaphore is
// created once and shared by every request the returned middleware handles,
// so mounting it on multiple routers enforces a single global cap. A limit
// <= 0 disables the middleware.
func ConcurrencyLimit(limit int) func(http.Handler) http.Handler {
	if limit <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	semaphore := make(chan struct{}, limit)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				json.NewEncoder(w).Encode(map[string]string{"error": "server overloaded"})
			}
		})
	}
}
