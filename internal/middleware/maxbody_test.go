package middleware_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-api-gateway/internal/middleware"
)

func TestMaxBody_UnderLimit(t *testing.T) {
	body := strings.NewReader("small")
	var readErr error

	handler := middleware.MaxBody(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.NoError(t, readErr)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMaxBody_ExactLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat("x", 100))
	var readErr error

	handler := middleware.MaxBody(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.NoError(t, readErr)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMaxBody_ContentLengthOverLimit(t *testing.T) {
	called := false
	handler := middleware.MaxBody(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 2048)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.False(t, called, "downstream handler must not be invoked when Content-Length exceeds limit")

	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "request body too large", body["error"])
}

func TestMaxBody_ChunkedOverLimit(t *testing.T) {
	var readErr error
	handler := middleware.MaxBody(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 2048)))
	req.ContentLength = -1 // simulate chunked transfer
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Error(t, readErr)
	_, ok := errors.AsType[*http.MaxBytesError](readErr)
	assert.True(t, ok, "expected *http.MaxBytesError, got %T", readErr)
}
