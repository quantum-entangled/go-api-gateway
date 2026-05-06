package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-api-gateway/internal/config"
	"go-api-gateway/internal/middleware"
	"go-api-gateway/internal/ratelimit"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPublicKey_ValidRSA(t *testing.T) {
	_, path := writeTestKeypair(t)
	pub, err := loadPublicKey(path)

	require.NoError(t, err)
	assert.NotNil(t, pub)
}

func TestLoadPublicKey_MissingFile(t *testing.T) {
	_, err := loadPublicKey(filepath.Join(t.TempDir(), "bad.pem"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading public key")
}

func TestLoadPublicKey_MalformedPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a pem file"), 0o600))

	_, err := loadPublicKey(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM block")
}

func TestLoadPublicKey_RejectsNonRSA(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	path := filepath.Join(t.TempDir(), "ecdsa.pem")
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))

	_, err = loadPublicKey(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not RSA")
}

func TestBuildServiceLimiter_NoLimiterWhenNeitherSet(t *testing.T) {
	svc := config.ServiceConfig{Name: "svc", Prefix: "/svc"}
	cfg := &config.GatewayConfig{}
	limiter, keyFunc, err := buildServiceLimiter(context.Background(), svc, cfg, nil)

	require.NoError(t, err)
	assert.Nil(t, limiter)
	assert.Nil(t, keyFunc)
}

func TestBuildServiceLimiter_GlobalConfigUsesMemoryWhenNoRedisClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := config.ServiceConfig{Name: "svc", Prefix: "/svc"}
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{
			Backend:         "memory",
			Rate:            10,
			Burst:           20,
			CleanupInterval: time.Minute,
			CleanupMaxIdle:  3 * time.Minute,
		},
	}
	limiter, keyFunc, err := buildServiceLimiter(ctx, svc, cfg, nil)

	require.NoError(t, err)
	assert.IsType(t, &ratelimit.MemoryLimiter{}, limiter)
	assert.NotNil(t, keyFunc)
}

func TestBuildServiceLimiter_RedisClientPrefersRedis(t *testing.T) {
	svc := config.ServiceConfig{Name: "svc", Prefix: "/svc"}
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{Backend: "redis", Rate: 10, Burst: 20},
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	limiter, _, err := buildServiceLimiter(context.Background(), svc, cfg, client)

	require.NoError(t, err)
	assert.IsType(t, &ratelimit.RedisLimiter{}, limiter)
}

func TestBuildServiceLimiter_ServiceOverrideWinsOverGlobal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := config.ServiceConfig{
		Name:      "svc",
		Prefix:    "/svc",
		RateLimit: &config.ServiceRateLimitConfig{Rate: 100, Burst: 200, KeyBy: "ip"},
	}
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{
			Backend:         "memory",
			Rate:            1,
			Burst:           1,
			CleanupInterval: time.Minute,
			CleanupMaxIdle:  3 * time.Minute,
		},
	}
	limiter, keyFunc, err := buildServiceLimiter(ctx, svc, cfg, nil)

	require.NoError(t, err)
	assert.IsType(t, &ratelimit.MemoryLimiter{}, limiter)
	require.NotNil(t, keyFunc)

	for range 100 {
		ok, allowErr := limiter.Allow(context.Background(), "k")
		require.NoError(t, allowErr)
		require.True(t, ok)
	}
}

func TestBuildServiceLimiter_JWTSubKeyingProducesLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	priv, _ := writeTestKeypair(t)
	svc := config.ServiceConfig{
		Name:      "svc",
		Prefix:    "/svc",
		Auth:      true,
		RateLimit: &config.ServiceRateLimitConfig{Rate: 10, Burst: 20, KeyBy: "jwt_sub"},
	}
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{
			CleanupInterval: time.Minute,
			CleanupMaxIdle:  3 * time.Minute,
		},
	}
	limiter, keyFunc, err := buildServiceLimiter(ctx, svc, cfg, nil)

	require.NoError(t, err)
	assert.IsType(t, &ratelimit.MemoryLimiter{}, limiter)
	require.NotNil(t, keyFunc)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"user"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)

	var key string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		key = keyFunc(r)
	})
	handler := middleware.JWTAuth(&priv.PublicKey)(inner)

	req := httptest.NewRequest("GET", "/svc/x", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "user1", key)
}

func TestBuildRedisClient_NilWhenNoRateLimit(t *testing.T) {
	cfg := &config.GatewayConfig{}
	client, err := buildRedisClient(context.Background(), cfg)

	require.NoError(t, err)
	assert.Nil(t, client)
}

func TestBuildRedisClient_NilWhenMemoryBackend(t *testing.T) {
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{Backend: "memory", Rate: 10, Burst: 20},
	}
	client, err := buildRedisClient(context.Background(), cfg)

	require.NoError(t, err)
	assert.Nil(t, client)
}

func TestBuildRedisOptions_TLSEnabledSetsConfigWithTLS12Min(t *testing.T) {
	rc := config.RedisConfig{Addr: "redis:6379", TLS: true}
	opts := buildRedisOptions(rc, "password")

	require.NotNil(t, opts.TLSConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), opts.TLSConfig.MinVersion)
}

func TestBuildRedisOptions_TLSDisabledLeavesConfigNil(t *testing.T) {
	rc := config.RedisConfig{Addr: "redis:6379"}
	opts := buildRedisOptions(rc, "password")

	assert.Nil(t, opts.TLSConfig)
}

func TestBuildRedisOptions_PassesThroughTimeouts(t *testing.T) {
	rc := config.RedisConfig{
		Addr:         "redis:6379",
		PoolSize:     42,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  750 * time.Millisecond,
		WriteTimeout: 1500 * time.Millisecond,
	}
	opts := buildRedisOptions(rc, "password")

	assert.Equal(t, "redis:6379", opts.Addr)
	assert.Equal(t, "password", opts.Password)
	assert.Equal(t, 42, opts.PoolSize)
	assert.Equal(t, 2*time.Second, opts.DialTimeout)
	assert.Equal(t, 750*time.Millisecond, opts.ReadTimeout)
	assert.Equal(t, 1500*time.Millisecond, opts.WriteTimeout)
}

func TestBuildRedisClient_PingFailureSurfaces(t *testing.T) {
	cfg := &config.GatewayConfig{
		RateLimit: &config.RateLimitConfig{
			Backend: "redis",
			Rate:    10,
			Burst:   20,
			Redis: config.RedisConfig{
				Addr:        "127.0.0.1:1",
				DialTimeout: 100 * time.Millisecond,
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := buildRedisClient(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis ping")
}
