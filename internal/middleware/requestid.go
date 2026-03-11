package middleware

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
)

// ctxKey is an unexported type for context keys in this package.
// Using a struct type (not a string) prevents collisions with keys
// from other packages, even if they use the same name.
type ctxKey struct{}

var requestIDKey ctxKey

func newContext(ctx context.Context, reqID string) context.Context {
	return context.WithValue(ctx, requestIDKey, reqID)
}

// FromContext extracts the request ID from the context.
func FromContext(ctx context.Context) (string, bool) {
	ctxValue, ok := ctx.Value(requestIDKey).(string)
	return ctxValue, ok
}

// RequestID generates a UUID v4, stores it in the request context,
// and sets the X-Request-ID response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uuid, err := newUUID()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		ctx := newContext(r.Context(), uuid)
		w.Header().Set("X-Request-ID", uuid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newUUID() (string, error) {
	uuid := make([]byte, 16)

	_, err := rand.Read(uuid)
	if err != nil {
		return "", fmt.Errorf("UUID creation failed: %w", err)
	}

	// RFC 9562: byte 6 upper nibble = 0100 (version 4),
	// byte 8 upper two bits = 10 (variant 1). The rest is random.
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}
