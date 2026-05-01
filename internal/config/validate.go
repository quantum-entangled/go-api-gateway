package config

import (
	"fmt"
	"time"
)

func validate(cfg *GatewayConfig) error {
	if err := validateServices(cfg); err != nil {
		return err
	}
	if err := validateRateLimit(cfg.RateLimit); err != nil {
		return err
	}
	if err := validateCircuitBreaker(cfg.CircuitBreaker); err != nil {
		return err
	}
	applyHealthCheckDefaults(&cfg.HealthCheck)
	applyCompressionDefaults(&cfg.Compression)
	applyServerLimitDefaults(cfg)
	applyTransportDefaults(&cfg.Transport)
	return nil
}

func validateServices(cfg *GatewayConfig) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	seenName := make(map[string]bool, len(cfg.Services))
	seenPrefix := make(map[string]bool, len(cfg.Services))

	for i := range cfg.Services {
		svc := &cfg.Services[i]

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

		if len(svc.RequiredRoles) > 0 && !svc.Auth {
			return fmt.Errorf("service %q: required_roles requires auth: true", svc.Name)
		}

		if err := validateServiceRateLimit(svc); err != nil {
			return err
		}
		applyServiceCacheDefaults(svc)
	}

	return nil
}

func validateServiceRateLimit(svc *ServiceConfig) error {
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

func validateRateLimit(rl *RateLimitConfig) error {
	if rl == nil {
		return nil
	}
	if rl.Rate <= 0 {
		return fmt.Errorf("rate_limit.rate must be positive")
	}
	if rl.Burst <= 0 {
		return fmt.Errorf("rate_limit.burst must be positive")
	}
	if rl.Backend == "" {
		rl.Backend = "memory"
	}
	switch rl.Backend {
	case "memory":
	case "redis":
		if rl.Redis.Addr == "" {
			return fmt.Errorf("rate_limit.redis.addr is required when backend is redis")
		}
	default:
		return fmt.Errorf("rate_limit.backend must be \"memory\" or \"redis\" (got %q)", rl.Backend)
	}
	if rl.CleanupInterval <= 0 {
		rl.CleanupInterval = 1 * time.Minute
	}
	if rl.CleanupMaxIdle <= 0 {
		rl.CleanupMaxIdle = 3 * time.Minute
	}
	return nil
}

func validateCircuitBreaker(cb *CBConfig) error {
	if cb == nil {
		return nil
	}
	if cb.MaxFailures <= 0 {
		return fmt.Errorf("circuit_breaker.max_failures must be positive")
	}
	if cb.Timeout <= 0 {
		return fmt.Errorf("circuit_breaker.timeout must be positive")
	}
	return nil
}

func applyServiceCacheDefaults(svc *ServiceConfig) {
	cc := svc.Cache
	if cc == nil {
		return
	}
	if cc.TTL <= 0 {
		cc.TTL = 60 * time.Second
	}
	if cc.MaxEntries <= 0 {
		cc.MaxEntries = 1024
	}
	if cc.MaxBytes <= 0 {
		cc.MaxBytes = 16 << 20 // 16 MB
	}
}

func applyHealthCheckDefaults(hc *HealthCheckConfig) {
	if hc.Path == "" {
		hc.Path = "/healthz"
	}
	if hc.Interval <= 0 {
		hc.Interval = 5 * time.Second
	}
}

func applyCompressionDefaults(cmp *CompressionConfig) {
	if cmp.Enabled && cmp.MinBytes <= 0 {
		cmp.MinBytes = 1024
	}
}

func applyServerLimitDefaults(cfg *GatewayConfig) {
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20 // 1 MB
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 32 << 10 // 32 KB
	}
}

func applyTransportDefaults(t *TransportConfig) {
	if t.MaxIdleConns <= 0 {
		t.MaxIdleConns = 1000
	}
	if t.MaxIdleConnsPerHost <= 0 {
		t.MaxIdleConnsPerHost = 200
	}
	if t.IdleConnTimeout <= 0 {
		t.IdleConnTimeout = 90 * time.Second
	}
	if t.DialTimeout <= 0 {
		t.DialTimeout = 5 * time.Second
	}
	if t.TLSHandshakeTimeout <= 0 {
		t.TLSHandshakeTimeout = 5 * time.Second
	}
}
