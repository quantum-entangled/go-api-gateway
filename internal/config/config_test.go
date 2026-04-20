package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

const validConfig = `
port: 9090
max_body_bytes: 32768
max_header_bytes: 16384
max_in_flight: 300

rate_limit:
  enabled: true
  rate: 10
  burst: 20
  cleanup_interval: 1m
  cleanup_max_idle: 3m

health_check:
  interval: 5s
  path: /healthz

circuit_breaker:
  enabled: true
  max_failures: 3
  timeout: 10s

services:
  - name: catalog
    prefix: /catalog
    upstreams:
      - http://localhost:8081
      - http://localhost:8082
    auth: false

  - name: orders
    prefix: /orders
    upstreams:
      - http://localhost:8083
    auth: true
`

const minimalServiceConfig = `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`

func TestLoad(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel:4318")
	t.Setenv("JWT_PUBLIC_KEY_PATH", "/keys/dev.pem")

	cfg, err := Load(writeConfig(t, validConfig))
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, int64(32768), cfg.MaxBodyBytes)
	assert.Equal(t, 16384, cfg.MaxHeaderBytes)
	assert.Equal(t, 300, cfg.MaxInFlight)
	assert.Equal(t, "otel:4318", cfg.OTelEndpoint)
	assert.Equal(t, "/keys/dev.pem", cfg.JWTPublicKey)

	assert.True(t, cfg.RateLimit.Enabled)
	assert.Equal(t, 10.0, cfg.RateLimit.Rate)
	assert.Equal(t, 20, cfg.RateLimit.Burst)

	assert.True(t, cfg.CircuitBreaker.Enabled)
	assert.Equal(t, 3, cfg.CircuitBreaker.MaxFailures)

	require.Len(t, cfg.Services, 2)
	assert.Equal(t, "catalog", cfg.Services[0].Name)
	assert.Equal(t, "/catalog", cfg.Services[0].Prefix)
	assert.Equal(t, []string{"http://localhost:8081", "http://localhost:8082"}, cfg.Services[0].Upstreams)
	assert.False(t, cfg.Services[0].Auth)

	assert.Equal(t, "orders", cfg.Services[1].Name)
	assert.True(t, cfg.Services[1].Auth)
}

func TestLoad_DefaultPort(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port)
}

func TestLoad_DefaultBodyAndHeaderLimits(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Equal(t, int64(1<<20), cfg.MaxBodyBytes)
	assert.Equal(t, 32<<10, cfg.MaxHeaderBytes)
}

func TestLoad_DefaultHealthCheck(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Equal(t, "/healthz", cfg.HealthCheck.Path)
	assert.Positive(t, cfg.HealthCheck.Interval)
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/gateway.yaml")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config file")
}

func TestLoad_InvalidYAML(t *testing.T) {
	_, err := Load(writeConfig(t, "{{invalid"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config file")
}

func TestLoad_NoServices(t *testing.T) {
	_, err := Load(writeConfig(t, `port: 8080`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one service is required")
}

func TestLoad_ServiceMissingName(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestLoad_ServiceMissingPrefix(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prefix is required")
}

func TestLoad_ServiceNoUpstreams(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one upstream is required")
}

func TestLoad_DuplicatePrefix(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc-a
    prefix: /svc
    upstreams: [http://localhost:8081]
  - name: svc-b
    prefix: /svc
    upstreams: [http://localhost:8082]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate prefix")
}

func TestLoad_RateLimitInvalid(t *testing.T) {
	_, err := Load(writeConfig(t, `
rate_limit:
  enabled: true
  rate: 0
  burst: 10
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.rate must be positive")
}

func TestLoad_CircuitBreakerInvalid(t *testing.T) {
	_, err := Load(writeConfig(t, `
circuit_breaker:
  enabled: true
  max_failures: 0
  timeout: 10s
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit_breaker.max_failures must be positive")
}

func TestLoad_RateLimitDefaultBackendIsMemory(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
rate_limit:
  enabled: true
  rate: 10
  burst: 20
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.Equal(t, "memory", cfg.RateLimit.Backend)
	assert.Positive(t, cfg.RateLimit.CleanupInterval)
	assert.Positive(t, cfg.RateLimit.CleanupMaxIdle)
}

func TestLoad_RateLimitRedisRequiresAddr(t *testing.T) {
	_, err := Load(writeConfig(t, `
rate_limit:
  enabled: true
  backend: redis
  rate: 10
  burst: 20
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.redis.addr is required")
}

func TestLoad_RateLimitUnknownBackend(t *testing.T) {
	_, err := Load(writeConfig(t, `
rate_limit:
  enabled: true
  backend: dynamodb
  rate: 10
  burst: 20
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.backend")
}

func TestLoad_RateLimitRedisOptionsRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
rate_limit:
  enabled: true
  backend: redis
  rate: 10
  burst: 20
  redis:
    addr: redis:6379
    pool_size: 42
    dial_timeout: 2s
    read_timeout: 750ms
    write_timeout: 1500ms
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.Equal(t, "redis:6379", cfg.RateLimit.Redis.Addr)
	assert.Equal(t, 42, cfg.RateLimit.Redis.PoolSize)
	assert.Equal(t, 2*time.Second, cfg.RateLimit.Redis.DialTimeout)
	assert.Equal(t, 750*time.Millisecond, cfg.RateLimit.Redis.ReadTimeout)
	assert.Equal(t, 1500*time.Millisecond, cfg.RateLimit.Redis.WriteTimeout)
}

func TestLoad_RedisPasswordFromEnv(t *testing.T) {
	t.Setenv("REDIS_PASSWORD", "s3cret")

	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Equal(t, "s3cret", cfg.RedisPassword)
}

func TestLoad_DisabledRateLimitSkipsValidation(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
rate_limit:
  enabled: false
  rate: 0
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.False(t, cfg.RateLimit.Enabled)
}

func TestLoad_DisabledCircuitBreakerSkipsValidation(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
circuit_breaker:
  enabled: false
  max_failures: 0
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.False(t, cfg.CircuitBreaker.Enabled)
}
