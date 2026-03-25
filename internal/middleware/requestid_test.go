package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"go-api-gateway/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID_SetsHeader(t *testing.T) {
	var capturedID string
	var capturedOK bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID, capturedOK = middleware.FromContext(r.Context())
		require.True(t, capturedOK, "context ID is not set")
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.RequestID(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	headerID := rec.Header().Get("X-Request-ID")
	require.NotEmpty(t, headerID, "X-Request-ID header must be set")
	assert.Equal(t, headerID, capturedID, "context ID must match header ID")
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.RequestID(inner)
	reqFirst := httptest.NewRequest("GET", "/", nil)
	recFirst := httptest.NewRecorder()
	reqSecond := httptest.NewRequest("GET", "/", nil)
	recSecond := httptest.NewRecorder()

	handler.ServeHTTP(recFirst, reqFirst)
	firstID := recFirst.Header().Get("X-Request-ID")

	handler.ServeHTTP(recSecond, reqSecond)
	secondID := recSecond.Header().Get("X-Request-ID")

	assert.NotEqual(t, firstID, secondID)
}

func TestFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	_, ok := middleware.FromContext(ctx)
	assert.False(t, ok)
}

func TestRequestID_UUIDFormat(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware.RequestID(inner)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	uuid := rec.Header().Get("X-Request-ID")
	match, err := regexp.MatchString("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$", uuid)
	if err != nil {
		t.Fatalf("regexp.MatchString() error: %v", err)
	}

	assert.True(t, match, "UUID is not formatted correctly (v4)")
}
