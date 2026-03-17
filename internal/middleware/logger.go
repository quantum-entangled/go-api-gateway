package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader captures the status code and delegates to the wrapped ResponseWriter.
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Write captures the number of bytes written and delegates to the wrapped ResponseWriter.
func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Logger returns a middleware that logs every request with method, path,
// status, duration, bytes written, and request ID.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startTime := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rw, r)

			// Turn nanoseconds to fractional ms, as Milliseconds() truncates to 0 for sub-ms requests.
			durationMs := float64(time.Since(startTime).Nanoseconds()) / 1e6
			requestID, _ := FromContext(r.Context())

			var level func(msg string, args ...any)
			switch {
			case rw.status >= 500:
				level = log.Error
			case rw.status >= 400:
				level = log.Warn
			default:
				level = log.Info
			}
			level(
				"request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Float64("duration_ms", durationMs),
				slog.Int("bytes", rw.bytes),
				slog.String("request_id", requestID),
			)
		})
	}
}
