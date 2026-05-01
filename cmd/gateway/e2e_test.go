package main

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-api-gateway/internal/config"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGateway_E2E drives the middleware chain over a real HTTP listener.
func TestGateway_E2E(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"path":%q,"hit":%d}`, r.URL.Path, upstreamHits.Load())
	}))
	t.Cleanup(upstream.Close)

	privKey, pubKeyPath := writeTestKeypair(t)
	cfg := &config.GatewayConfig{
		MaxBodyBytes:   1 << 20,
		MaxInFlight:    100,
		Compression:    config.CompressionConfig{Enabled: true, MinBytes: 1},
		HealthCheck:    config.HealthCheckConfig{Interval: time.Minute, Path: "/healthz"},
		CircuitBreaker: &config.CBConfig{MaxFailures: 5, Timeout: 30 * time.Second},
		JWTPublicKey:   pubKeyPath,
		Services: []config.ServiceConfig{{
			Name:      "catalog",
			Prefix:    "/catalog",
			Upstreams: []string{upstream.URL},
			Auth:      true,
			Cache:     &config.ServiceCacheConfig{TTL: time.Minute, MaxEntries: 16, MaxBytes: 1 << 20},
		}},
	}
	router, err := buildRouter(context.Background(), cfg, slog.Default())
	require.NoError(t, err)

	upstreamHits.Store(0) // reset startup probes
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	client := &http.Client{Timeout: 5 * time.Second}

	t.Run("unauthenticated request is rejected", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/catalog/items")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Zero(t, upstreamHits.Load())
	})

	token := signE2EToken(t, privKey)

	t.Run("authenticated request reaches upstream", func(t *testing.T) {
		resp, body := authedGet(t, client, gateway.URL+"/catalog/items", token, false)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, string(body), `"path":"/items"`)
		assert.Equal(t, int32(1), upstreamHits.Load())
		assert.NotEmpty(t, resp.Header.Get("X-Request-Id"))
	})

	t.Run("second identical request is served from cache", func(t *testing.T) {
		resp, body := authedGet(t, client, gateway.URL+"/catalog/items", token, false)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, string(body), `"hit":1`)
		assert.Equal(t, int32(1), upstreamHits.Load())
	})

	t.Run("Accept-Encoding gzip yields a compressed body", func(t *testing.T) {
		resp, body := authedGet(t, client, gateway.URL+"/catalog/items", token, true)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Contains(t, string(body), `"path":"/items"`)
	})

}

// authedGet sends an authenticated GET, transparently decoding gzip if requested.
func authedGet(t *testing.T, client *http.Client, url, token string, gzipReq bool) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if gzipReq {
		req.Header.Set("Accept-Encoding", "gzip")
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		require.NoError(t, err)
		t.Cleanup(func() { _ = gz.Close() })
		reader = gz
	}
	body, err := io.ReadAll(reader)
	require.NoError(t, err)

	return resp, body
}

func signE2EToken(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":   "e2e-user",
		"roles": []string{"user"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

func writeTestKeypair(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	path := filepath.Join(t.TempDir(), "jwt_pub.pem")
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
	return priv, path
}
