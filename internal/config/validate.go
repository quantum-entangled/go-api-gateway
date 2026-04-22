package config

import (
	"fmt"
	"time"
)

func validate(cfg *GatewayConfig) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	seenPrefix := make(map[string]bool, len(cfg.Services))
	seenName := make(map[string]bool, len(cfg.Services))
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
		if seenName[svc.Name] {
			return fmt.Errorf("service %q: duplicate name", svc.Name)
		}
		seenName[svc.Name] = true
		if seenPrefix[svc.Prefix] {
			return fmt.Errorf("service %q: duplicate prefix %q", svc.Name, svc.Prefix)
		}
		seenPrefix[svc.Prefix] = true

		if err := validateServiceRateLimit(svc); err != nil {
			return err
		}
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

func validateServiceRateLimit(svc ServiceConfig) error {
	rl := svc.RateLimit
	if rl == nil {
		return nil
	}
	if rl.Rate <= 0 {
		return fmt.Errorf("service %q: rate_limit.rate must be positive", svc.Name)
	}
	if rl.Burst <= 0 {
		return fmt.Errorf("service %q: rate_limit.burst must be positive", svc.Name)
	}

	switch rl.KeyBy {
	case "", "ip":
		rl.KeyBy = "ip"
	case "jwt_sub":
		if !svc.Auth {
			return fmt.Errorf("service %q: rate_limit.key_by=jwt_sub requires auth: true", svc.Name)
		}
	default:
		return fmt.Errorf("service %q: rate_limit.key_by must be \"ip\" or \"jwt_sub\" (got %q)", svc.Name, rl.KeyBy)
	}

	return nil
}
