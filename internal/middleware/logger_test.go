package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-api-gateway/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_LogsRequest(t *testing.T) {
	// Stack: RequestID -> Logger -> inner handler
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})
	handler := middleware.RequestID(middleware.Logger(log)(inner))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test-path", nil)
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err, "log output must be valid JSON")

	assert.Equal(t, "GET", entry["method"])
	assert.Equal(t, "/test-path", entry["path"])
	assert.Equal(t, float64(200), entry["status"])
	assert.Equal(t, float64(5), entry["bytes"])
	assert.NotEmpty(t, entry["request_id"])
	assert.NotEmpty(t, entry["duration_ms"])
}

func TestLogger_WarnOn4xx(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := middleware.Logger(logger)(inner)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err, "log output must be valid JSON")

	assert.Equal(t, "WARN", entry["level"], "the log level is not WARN")
}

func TestLogger_ErrorOn5xx(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := middleware.Logger(logger)(inner)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err, "log output must be valid JSON")

	assert.Equal(t, "ERROR", entry["level"], "the log level is not ERROR")
}

func TestLogger_DefaultStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"check": "pass"})
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := middleware.Logger(logger)(inner)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err, "log output must be valid JSON")

	assert.Equal(t, float64(200), entry["status"], "the log status is not 200")
}
