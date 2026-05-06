package middleware

import (
	"encoding/json"
	"net/http"
)

// MaxBody caps incoming request body size. Requests with a Content-Length over
// the limit are rejected with 413 before reaching the handler. Streamed or
// chunked bodies are capped via http.MaxBytesReader as they're read. Downstream
// handlers should translate *http.MaxBytesError into 413.
func MaxBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "request body too large"})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
