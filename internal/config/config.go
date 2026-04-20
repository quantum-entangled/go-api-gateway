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
	Name      string   `yaml:"name"`
	Prefix    string   `yaml:"prefix"`
	Upstreams []string `yaml:"upstreams"`
	Auth      bool     `yaml:"auth"`
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

func validate(cfg *GatewayConfig) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	seen := make(map[string]bool, len(cfg.Services))
	for i, svc := range cfg.Services {
		if svc.Name == "" {
			return fmt.Errorf("service[%d]: name is required", i)
		}
		if svc.Prefix == "" {
			return fmt.Errorf("service %q: prefix is required", svc.Name)
		}
		if len(svc.Upstreams) == 0 {
			return fmt.Errorf("service %q: at least one upstream is required", svc.Name)
		}
		if seen[svc.Prefix] {
			return fmt.Errorf("service %q: duplicate prefix %q", svc.Name, svc.Prefix)
		}
		seen[svc.Prefix] = true
	}

	if cfg.RateLimit.Enabled {
		if cfg.RateLimit.Rate <= 0 {
			return fmt.Errorf("rate_limit.rate must be positive")
		}
		if cfg.RateLimit.Burst <= 0 {
			return fmt.Errorf("rate_limit.burst must be positive")
		}
		if cfg.RateLimit.Backend == "" {
			cfg.RateLimit.Backend = "memory"
		}
		if cfg.RateLimit.CleanupInterval <= 0 {
			cfg.RateLimit.CleanupInterval = 1 * time.Minute
		}
		if cfg.RateLimit.CleanupMaxIdle <= 0 {
			cfg.RateLimit.CleanupMaxIdle = 3 * time.Minute
		}
		switch cfg.RateLimit.Backend {
		case "memory":
		case "redis":
			if cfg.RateLimit.Redis.Addr == "" {
				return fmt.Errorf("rate_limit.redis.addr is required when backend is redis")
			}
		default:
			return fmt.Errorf("rate_limit.backend must be \"memory\" or \"redis\" (got %q)", cfg.RateLimit.Backend)
		}
	}

	if cfg.CircuitBreaker.Enabled {
		if cfg.CircuitBreaker.MaxFailures <= 0 {
			return fmt.Errorf("circuit_breaker.max_failures must be positive")
		}
		if cfg.CircuitBreaker.Timeout <= 0 {
			return fmt.Errorf("circuit_breaker.timeout must be positive")
		}
	}

	if cfg.HealthCheck.Path == "" {
		cfg.HealthCheck.Path = "/healthz"
	}
	if cfg.HealthCheck.Interval <= 0 {
		cfg.HealthCheck.Interval = 5 * time.Second
	}

	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20 // 1 MB
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 32 << 10 // 32 KB
	}

	if cfg.Transport.MaxIdleConns <= 0 {
		cfg.Transport.MaxIdleConns = 1000
	}
	if cfg.Transport.MaxIdleConnsPerHost <= 0 {
		cfg.Transport.MaxIdleConnsPerHost = 200
	}
	if cfg.Transport.IdleConnTimeout <= 0 {
		cfg.Transport.IdleConnTimeout = 90 * time.Second
	}
	if cfg.Transport.DialTimeout <= 0 {
		cfg.Transport.DialTimeout = 5 * time.Second
	}
	if cfg.Transport.TLSHandshakeTimeout <= 0 {
		cfg.Transport.TLSHandshakeTimeout = 5 * time.Second
	}

	return nil
}
