package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"go-api-gateway/internal/circuitbreaker"
	"go-api-gateway/internal/config"
	"go-api-gateway/internal/health"
	"go-api-gateway/internal/loadbalancer"
	"go-api-gateway/internal/metrics"
	"go-api-gateway/internal/middleware"
	"go-api-gateway/internal/proxy"
	"go-api-gateway/internal/ratelimit"
)

// buildRouter assembles the gateway router. Returns an error instead of
// exiting so both main and tests can drive it.
func buildRouter(ctx context.Context, cfg *config.GatewayConfig, logger *slog.Logger) (*chi.Mux, error) {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.MaxBody(cfg.MaxBodyBytes))

	// When no OTel provider is registered, these calls are no-ops.
	m, err := metrics.NewMetrics(otel.GetMeterProvider().Meter("go-api-gateway"))
	if err != nil {
		return nil, fmt.Errorf("creating metrics: %w", err)
	}
	r.Use(m.Middleware())
	r.Use(middleware.Tracing(
		otel.GetTracerProvider().Tracer("go-api-gateway"),
		otel.GetTextMapPropagator(),
	))
	r.Use(middleware.ConcurrencyLimit(cfg.MaxInFlight))

	edgeLimiter := ratelimit.NewMemoryLimiter(ctx, "", rate.Limit(10), 20, time.Minute, 3*time.Minute)
	edgeRateLimit := middleware.RateLimit(edgeLimiter, middleware.KeyByIP, logger)
	r.NotFound(edgeRateLimit(notFoundHandler()).ServeHTTP)
	r.MethodNotAllowed(edgeRateLimit(methodNotAllowedHandler()).ServeHTTP)

	r.With(edgeRateLimit).Get("/healthz", healthzHandler())

	var publicKey *rsa.PublicKey
	if cfg.JWTPublicKey != "" {
		publicKey, err = loadPublicKey(cfg.JWTPublicKey)
		if err != nil {
			return nil, fmt.Errorf("loading JWT public key: %w", err)
		}
	}

	redisClient, err := buildRedisClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("building redis client: %w", err)
	}

	type serviceCheck struct {
		name    string
		checker *health.Checker
	}
	checks := make([]serviceCheck, 0, len(cfg.Services))

	for _, svc := range cfg.Services {
		if svc.Auth && publicKey == nil {
			return nil, fmt.Errorf("service %q requires auth but JWT_PUBLIC_KEY_PATH is not set", svc.Name)
		}

		handler, checker := buildServiceHandler(svc, cfg, logger)
		checks = append(checks, serviceCheck{name: svc.Name, checker: checker})

		limiter, keyFunc, err := buildServiceLimiter(ctx, svc, cfg, redisClient)
		if err != nil {
			return nil, fmt.Errorf("building rate limiter for service %q: %w", svc.Name, err)
		}

		r.Route(svc.Prefix, func(r chi.Router) {
			if svc.Auth {
				r.Use(middleware.JWTAuth(publicKey))
			}
			if limiter != nil {
				r.Use(middleware.RateLimit(limiter, keyFunc, logger))
			}
			if cfg.Compression.Enabled {
				r.Use(middleware.Compress(cfg.Compression.MinBytes))
			}
			r.Mount("/", http.StripPrefix(svc.Prefix, handler))
		})

		logger.Info(
			"registered service",
			"name", svc.Name,
			"prefix", svc.Prefix,
			"upstreams", svc.Upstreams,
			"auth", svc.Auth,
			"rate_limited", limiter != nil,
		)
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	g, gctx := errgroup.WithContext(probeCtx)

	for _, sc := range checks {
		g.Go(func() error {
			if err := sc.checker.Probe(gctx); err != nil {
				return fmt.Errorf("service %q: %w", sc.name, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("startup probe: %w", err)
	}

	for _, sc := range checks {
		sc.checker.Start(ctx)
	}

	return r, nil
}

// buildRedisClient returns nil when rate limiting is disabled or uses memory.
// Pings at startup to surface misconfiguration before serving traffic.
func buildRedisClient(ctx context.Context, cfg *config.GatewayConfig) (*redis.Client, error) {
	if !cfg.RateLimit.Enabled || cfg.RateLimit.Backend != "redis" {
		return nil, nil
	}

	rc := cfg.RateLimit.Redis
	opts := &redis.Options{Addr: rc.Addr, Password: cfg.RedisPassword}
	if rc.PoolSize > 0 {
		opts.PoolSize = rc.PoolSize
	}
	if rc.DialTimeout > 0 {
		opts.DialTimeout = rc.DialTimeout
	}
	if rc.ReadTimeout > 0 {
		opts.ReadTimeout = rc.ReadTimeout
	}
	if rc.WriteTimeout > 0 {
		opts.WriteTimeout = rc.WriteTimeout
	}

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping at %s: %w", rc.Addr, err)
	}

	slog.Info("redis client: connected", "addr", rc.Addr)
	return client, nil
}

// buildServiceLimiter picks the service override over the global default,
// or returns (nil, nil, nil) when neither applies.
func buildServiceLimiter(
	ctx context.Context,
	svc config.ServiceConfig,
	cfg *config.GatewayConfig,
	redisClient *redis.Client,
) (ratelimit.Limiter, func(*http.Request) string, error) {
	var (
		r     float64
		b     int
		keyBy string
	)

	switch {
	case svc.RateLimit != nil:
		r = svc.RateLimit.Rate
		b = svc.RateLimit.Burst
		keyBy = svc.RateLimit.KeyBy
	case cfg.RateLimit.Enabled:
		r = cfg.RateLimit.Rate
		b = cfg.RateLimit.Burst
		keyBy = "ip"
	default:
		return nil, nil, nil
	}

	keyFunc := middleware.KeyByIP
	if keyBy == "jwt_sub" {
		keyFunc = middleware.KeyByJWTSub
	}
	prefix := "svc:" + svc.Name + ":"

	if redisClient != nil {
		return ratelimit.NewRedisLimiter(redisClient, prefix, r, b), keyFunc, nil
	}

	interval := cfg.RateLimit.CleanupInterval
	if interval <= 0 {
		interval = time.Minute
	}
	maxIdle := cfg.RateLimit.CleanupMaxIdle
	if maxIdle <= 0 {
		maxIdle = 3 * time.Minute
	}

	return ratelimit.NewMemoryLimiter(ctx, prefix, rate.Limit(r), b, interval, maxIdle), keyFunc, nil
}

func buildServiceHandler(
	svc config.ServiceConfig,
	cfg *config.GatewayConfig,
	logger *slog.Logger,
) (http.Handler, *health.Checker) {
	checker := health.NewChecker(svc.Upstreams, cfg.HealthCheck.Interval, cfg.HealthCheck.Path)

	lb := loadbalancer.NewRoundRobin(svc.Upstreams, checker)

	breakers := make(map[string]*circuitbreaker.Breaker, len(svc.Upstreams))
	for _, u := range svc.Upstreams {
		if cfg.CircuitBreaker.Enabled {
			breakers[u] = circuitbreaker.NewBreaker(
				cfg.CircuitBreaker.MaxFailures,
				cfg.CircuitBreaker.Timeout,
			)
		} else {
			// No-op: threshold unreachable, so the breaker never trips
			breakers[u] = circuitbreaker.NewBreaker(math.MaxInt, time.Hour)
		}
	}

	tc := proxy.TransportConfig{
		MaxIdleConns:        cfg.Transport.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.Transport.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.Transport.IdleConnTimeout,
		DialTimeout:         cfg.Transport.DialTimeout,
		TLSHandshakeTimeout: cfg.Transport.TLSHandshakeTimeout,
	}

	return proxy.NewHandler(lb, breakers, tc, logger), checker
}

func loadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading public key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing public key: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA")
	}

	return rsaPub, nil
}
