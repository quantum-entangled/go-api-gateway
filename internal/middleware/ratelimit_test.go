package middleware_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-api-gateway/internal/middleware"
	"go-api-gateway/internal/ratelimit"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func newTestLimiter(t *testing.T, r rate.Limit, b int) ratelimit.Limiter {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	t.Cleanup(func() { cancel() })
	return ratelimit.NewMemoryLimiter(ctx, "", r, b, time.Minute, time.Minute)
}

func TestRateLimit_AllowsWithinLimit(t *testing.T) {
	lim := newTestLimiter(t, rate.Limit(1), 3)
	handler := middleware.RateLimit(lim, middleware.KeyByIP, slog.Default())(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimit_RejectsOverLimit(t *testing.T) {
	lim := newTestLimiter(t, rate.Limit(1), 2)
	handler := middleware.RateLimit(lim, middleware.KeyByIP, slog.Default())(okHandler())

	for range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "rate limit exceeded")
}

func TestRateLimit_SetsRetryAfterHeader(t *testing.T) {
	lim := newTestLimiter(t, rate.Limit(1), 2)
	handler := middleware.RateLimit(lim, middleware.KeyByIP, slog.Default())(okHandler())

	for range 2 {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func TestKeyByJWTSub_ReturnsSubjectWhenClaimsPresent(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"reader"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	var key string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = middleware.KeyByJWTSub(r)
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.JWTAuth(&testKey.PublicKey)(inner)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "user1", key)
}

func TestKeyByJWTSub_FallsBackToIPWhenNoClaims(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	key := middleware.KeyByJWTSub(req)

	assert.Equal(t, "192.0.2.1", key)
}

func TestKeyByIP_StripPort(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	key := middleware.KeyByIP(req)

	assert.Equal(t, "192.0.2.1", key)

	req.RemoteAddr = "192.0.2.1"
	key = middleware.KeyByIP(req)

	assert.Equal(t, req.RemoteAddr, key)
}
