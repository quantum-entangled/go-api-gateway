package middleware_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-api-gateway/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompress_GzipsWhenClientAccepts(t *testing.T) {
	body := strings.Repeat("a", 2048)
	handler := middleware.Compress(1024)(okBodyHandler(body))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Header().Get("Vary"), "Accept-Encoding")
	assert.Empty(t, rec.Header().Get("Content-Length"))

	gz, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	defer func() { _ = gz.Close() }()
	decoded, err := io.ReadAll(gz)
	require.NoError(t, err)
	assert.Equal(t, body, string(decoded))
}

func TestCompress_PassthroughWhenClientDoesNotAccept(t *testing.T) {
	body := strings.Repeat("a", 2048)
	handler := middleware.Compress(1024)(okBodyHandler(body))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Header().Get("Vary"), "Accept-Encoding")
	assert.Equal(t, body, rec.Body.String())
}

func TestCompress_SkipsBelowMinSize(t *testing.T) {
	body := "tiny"
	handler := middleware.Compress(1024)(okBodyHandler(body))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Equal(t, body, rec.Body.String())
}

func TestCompress_SkipsWhenUpstreamAlreadyEncoded(t *testing.T) {
	body := strings.Repeat("a", 2048)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	handler := middleware.Compress(1024)(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Equal(t, body, rec.Body.String(), "body must pass through untouched when upstream pre-encoded")
}

func TestCompress_SkipsNonCompressibleContentType(t *testing.T) {
	body := strings.Repeat("\x00\x01\x02\x03", 1024)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	handler := middleware.Compress(1024)(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Equal(t, body, rec.Body.String())
}

func okBodyHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}
