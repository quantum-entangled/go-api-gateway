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
  rate: 10
  burst: 20
  cleanup_interval: 1m
  cleanup_max_idle: 3m

health_check:
  interval: 5s
  path: /healthz

circuit_breaker:
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
	t.Setenv("JWT_PUBLIC_KEY_PATH", "/keys/example.pem")

	cfg, err := Load(writeConfig(t, validConfig))
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, int64(32768), cfg.MaxBodyBytes)
	assert.Equal(t, 16384, cfg.MaxHeaderBytes)
	assert.Equal(t, 300, cfg.MaxInFlight)
	assert.Equal(t, "otel:4318", cfg.OTelEndpoint)
	assert.Equal(t, "/keys/example.pem", cfg.JWTPublicKey)

	require.NotNil(t, cfg.RateLimit)
	assert.Equal(t, 10.0, cfg.RateLimit.Rate)
	assert.Equal(t, 20, cfg.RateLimit.Burst)

	require.NotNil(t, cfg.CircuitBreaker)
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

func TestLoad_DuplicateName(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /a
    upstreams: [http://localhost:8081]
  - name: svc
    prefix: /b
    upstreams: [http://localhost:8082]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate name")
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
  rate: 10
  burst: 20
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	require.NotNil(t, cfg.RateLimit)
	assert.Equal(t, "memory", cfg.RateLimit.Backend)
	assert.Positive(t, cfg.RateLimit.CleanupInterval)
	assert.Positive(t, cfg.RateLimit.CleanupMaxIdle)
}

func TestLoad_RateLimitRedisRequiresAddr(t *testing.T) {
	_, err := Load(writeConfig(t, `
rate_limit:
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
	require.NotNil(t, cfg.RateLimit)
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

func TestLoad_OmittedRateLimitIsNil(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Nil(t, cfg.RateLimit)
}

func TestLoad_ServiceRateLimitOverride(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
services:
  - name: orders
    prefix: /orders
    upstreams: [http://localhost:8081]
    auth: true
    rate_limit:
      rate: 5
      burst: 10
      key_by: jwt_sub
`))

	require.NoError(t, err)
	require.NotNil(t, cfg.Services[0].RateLimit)
	assert.Equal(t, 5.0, cfg.Services[0].RateLimit.Rate)
	assert.Equal(t, 10, cfg.Services[0].RateLimit.Burst)
	assert.Equal(t, "jwt_sub", cfg.Services[0].RateLimit.KeyBy)
}

func TestLoad_ServiceRateLimitDefaultsKeyByToIP(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    rate_limit:
      rate: 5
      burst: 10
`))

	require.NoError(t, err)
	assert.Equal(t, "ip", cfg.Services[0].RateLimit.KeyBy)
}

func TestLoad_ServiceRateLimitInvalidRate(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    rate_limit:
      rate: 0
      burst: 10
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.rate must be positive")
}

func TestLoad_ServiceRateLimitInvalidBurst(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    rate_limit:
      rate: 5
      burst: 0
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.burst must be positive")
}

func TestLoad_ServiceRateLimitUnknownKeyBy(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    rate_limit:
      rate: 5
      burst: 10
      key_by: header
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate_limit.key_by")
}

func TestLoad_ServiceRateLimitJWTSubRequiresAuth(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    auth: false
    rate_limit:
      rate: 5
      burst: 10
      key_by: jwt_sub
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires auth: true")
}

func TestLoad_ServiceRequiredRolesRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
services:
  - name: orders
    prefix: /orders
    upstreams: [http://localhost:8081]
    auth: true
    required_roles: [admin, ops]
`))

	require.NoError(t, err)
	assert.Equal(t, []string{"admin", "ops"}, cfg.Services[0].RequiredRoles)
}

func TestLoad_ServiceRequiredRolesRequiresAuth(t *testing.T) {
	_, err := Load(writeConfig(t, `
services:
  - name: orders
    prefix: /orders
    upstreams: [http://localhost:8081]
    auth: false
    required_roles: [admin]
`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "required_roles requires auth: true")
}

func TestLoad_CompressionRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
compression:
  enabled: true
  min_bytes: 2048
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.True(t, cfg.Compression.Enabled)
	assert.Equal(t, 2048, cfg.Compression.MinBytes)
}

func TestLoad_CompressionDefaultMinBytes(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
compression:
  enabled: true
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.True(t, cfg.Compression.Enabled)
	assert.Equal(t, 1024, cfg.Compression.MinBytes)
}

func TestLoad_CompressionDisabledByDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.False(t, cfg.Compression.Enabled)
}

func TestLoad_ServiceCacheRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    cache:
      ttl: 30s
      max_entries: 512
      max_bytes: 4194304
`))

	require.NoError(t, err)
	require.NotNil(t, cfg.Services[0].Cache)
	assert.Equal(t, 30*time.Second, cfg.Services[0].Cache.TTL)
	assert.Equal(t, 512, cfg.Services[0].Cache.MaxEntries)
	assert.Equal(t, 4194304, cfg.Services[0].Cache.MaxBytes)
}

func TestLoad_ServiceCacheDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
    cache: {}
`))

	require.NoError(t, err)
	require.NotNil(t, cfg.Services[0].Cache)
	assert.Equal(t, 60*time.Second, cfg.Services[0].Cache.TTL)
	assert.Equal(t, 1024, cfg.Services[0].Cache.MaxEntries)
	assert.Equal(t, 16<<20, cfg.Services[0].Cache.MaxBytes)
}

func TestLoad_TransportRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
transport:
  max_idle_conns: 500
  max_idle_conns_per_host: 50
  idle_conn_timeout: 30s
  dial_timeout: 2s
  tls_handshake_timeout: 3s
services:
  - name: svc
    prefix: /svc
    upstreams: [http://localhost:8081]
`))

	require.NoError(t, err)
	assert.Equal(t, 500, cfg.Transport.MaxIdleConns)
	assert.Equal(t, 50, cfg.Transport.MaxIdleConnsPerHost)
	assert.Equal(t, 30*time.Second, cfg.Transport.IdleConnTimeout)
	assert.Equal(t, 2*time.Second, cfg.Transport.DialTimeout)
	assert.Equal(t, 3*time.Second, cfg.Transport.TLSHandshakeTimeout)
}

func TestLoad_TransportDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Equal(t, 1000, cfg.Transport.MaxIdleConns)
	assert.Equal(t, 200, cfg.Transport.MaxIdleConnsPerHost)
	assert.Equal(t, 90*time.Second, cfg.Transport.IdleConnTimeout)
	assert.Equal(t, 5*time.Second, cfg.Transport.DialTimeout)
	assert.Equal(t, 5*time.Second, cfg.Transport.TLSHandshakeTimeout)
}

func TestLoad_ServiceCacheAbsentByDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Nil(t, cfg.Services[0].Cache)
}

func TestLoad_OmittedCircuitBreakerIsNil(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalServiceConfig))

	require.NoError(t, err)
	assert.Nil(t, cfg.CircuitBreaker)
}
