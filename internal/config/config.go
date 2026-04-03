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
	RateLimit      RateLimitConfig   `yaml:"rate_limit"`
	HealthCheck    HealthCheckConfig `yaml:"health_check"`
	CircuitBreaker CBConfig          `yaml:"circuit_breaker"`
	Services       []ServiceConfig   `yaml:"services"`

	// Infra settings from environment variables (not in YAML).
	OTelEndpoint string `yaml:"-"`
	JWTPublicKey string `yaml:"-"`
}

// RateLimitConfig controls the global rate limiter.
// When Enabled is false, no rate limiting is applied.
type RateLimitConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Rate            float64       `yaml:"rate"`
	Burst           int           `yaml:"burst"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
	CleanupMaxIdle  time.Duration `yaml:"cleanup_max_idle"`
}

// HealthCheckConfig controls upstream health polling.
type HealthCheckConfig struct {
	Interval time.Duration `yaml:"interval"`
	Path     string        `yaml:"path"`
}

// CBConfig controls circuit breakers wrapping upstream calls.
// When Enabled is false, requests go directly to the upstream.
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

	return nil
}
