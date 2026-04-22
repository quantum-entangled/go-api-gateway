package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-api-gateway/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalConfig() *config.GatewayConfig {
	return &config.GatewayConfig{
		HealthCheck: config.HealthCheckConfig{Interval: time.Second, Path: "/healthz"},
		Services: []config.ServiceConfig{{
			Name:      "svc",
			Prefix:    "/svc",
			Upstreams: []string{"http://localhost:8081"},
		}},
	}
}

func TestBuildRouter_HealthzRegistered(t *testing.T) {
	router, err := buildRouter(context.Background(), minimalConfig(), slog.Default())
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestBuildRouter_NotFoundReturnsJSON(t *testing.T) {
	router, err := buildRouter(context.Background(), minimalConfig(), slog.Default())
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/nope", nil)
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "not found", body["error"])
}

func TestBuildRouter_MethodNotAllowedReturnsJSON(t *testing.T) {
	router, err := buildRouter(context.Background(), minimalConfig(), slog.Default())
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/healthz", nil)
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "method not allowed", body["error"])
}

func TestBuildRouter_ServicePrefixStripped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	cfg := minimalConfig()
	cfg.Services[0].Upstreams = []string{upstream.URL}

	router, err := buildRouter(context.Background(), cfg, slog.Default())
	require.NoError(t, err)

	// Wait for the health checker's first tick to mark the upstream healthy.
	require.Eventually(t, func() bool {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/svc/ping", nil)
		router.ServeHTTP(rec, req)
		return rec.Code == http.StatusTeapot
	}, 2*time.Second, 20*time.Millisecond)
}

func TestBuildRouter_AuthServiceRequiresPublicKey(t *testing.T) {
	cfg := minimalConfig()
	cfg.Services[0].Auth = true

	_, err := buildRouter(context.Background(), cfg, slog.Default())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires auth")
}
