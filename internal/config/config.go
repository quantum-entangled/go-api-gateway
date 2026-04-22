package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// GatewayConfig is the top-level configuration loaded from gateway.yaml.
type GatewayConfig struct {
	Port           int               `yaml:"port"`
	MaxBodyBytes   int64             `yaml:"max_body_bytes"`
	MaxHeaderBytes int               `yaml:"max_header_bytes"`
	MaxInFlight    int               `yaml:"max_in_flight"`
	RateLimit      RateLimitConfig   `yaml:"rate_limit"`
	HealthCheck    HealthCheckConfig `yaml:"health_check"`
	CircuitBreaker CBConfig          `yaml:"circuit_breaker"`
	Transport      TransportConfig   `yaml:"transport"`
	Services       []ServiceConfig   `yaml:"services"`

	// Infra settings from environment variables (not in YAML).
	OTelEndpoint  string `yaml:"-"`
	JWTPublicKey  string `yaml:"-"`
	RedisPassword string `yaml:"-"`
}

// TransportConfig controls the HTTP transport used for proxying to upstreams.
type TransportConfig struct {
	MaxIdleConns        int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
	IdleConnTimeout     time.Duration `yaml:"idle_conn_timeout"`
	DialTimeout         time.Duration `yaml:"dial_timeout"`
	TLSHandshakeTimeout time.Duration `yaml:"tls_handshake_timeout"`
}

// RateLimitConfig controls the global rate limiter.
// Backend selects memory (per-process) or redis (shared across instances).
type RateLimitConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Backend         string        `yaml:"backend"`
	Rate            float64       `yaml:"rate"`
	Burst           int           `yaml:"burst"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
	CleanupMaxIdle  time.Duration `yaml:"cleanup_max_idle"`
	Redis           RedisConfig   `yaml:"redis"`
}

// RedisConfig holds the options passed to redis.NewClient.
// Zero values fall back to go-redis defaults.
type RedisConfig struct {
	Addr         string        `yaml:"addr"`
	PoolSize     int           `yaml:"pool_size"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

// HealthCheckConfig controls upstream health polling.
type HealthCheckConfig struct {
	Interval time.Duration `yaml:"interval"`
	Path     string        `yaml:"path"`
}

// CBConfig controls circuit breakers wrapping upstream calls.
type CBConfig struct {
	Enabled     bool          `yaml:"enabled"`
	MaxFailures int           `yaml:"max_failures"`
	Timeout     time.Duration `yaml:"timeout"`
}

// ServiceConfig defines a single backend service the gateway proxies to.
type ServiceConfig struct {
	Name      string                  `yaml:"name"`
	Prefix    string                  `yaml:"prefix"`
	Upstreams []string                `yaml:"upstreams"`
	Auth      bool                    `yaml:"auth"`
	RateLimit *ServiceRateLimitConfig `yaml:"rate_limit,omitempty"`
}

// ServiceRateLimitConfig overrides the global rate limit for a service.
// KeyBy is "ip" or "jwt_sub"; jwt_sub requires Auth: true on the service.
type ServiceRateLimitConfig struct {
	Rate  float64 `yaml:"rate"`
	Burst int     `yaml:"burst"`
	KeyBy string  `yaml:"key_by"`
}

// Load reads gateway configuration from the given YAML file path and
// supplements it with infrastructure settings from environment variables.
func Load(path string) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg GatewayConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.Port == 0 {
		cfg.Port = 8080
	}

	cfg.OTelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	cfg.JWTPublicKey = os.Getenv("JWT_PUBLIC_KEY_PATH")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}
