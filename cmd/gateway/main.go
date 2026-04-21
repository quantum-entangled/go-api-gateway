package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"go-api-gateway/internal/circuitbreaker"
	"go-api-gateway/internal/config"
	"go-api-gateway/internal/health"
	"go-api-gateway/internal/loadbalancer"
	"go-api-gateway/internal/metrics"
	"go-api-gateway/internal/middleware"
	gatewayotel "go-api-gateway/internal/otel"
	"go-api-gateway/internal/proxy"
	"go-api-gateway/internal/ratelimit"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"golang.org/x/time/rate"
)

func main() {
	configPath := flag.String("config", "gateway.yaml", "path to gateway config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.OTelEndpoint != "" {
		sdk, err := gatewayotel.Setup(ctx, cfg.OTelEndpoint)
		if err != nil {
			slog.Error("failed to setup OTel", "error", err)
			os.Exit(1)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := sdk.Shutdown(shutdownCtx); err != nil {
				slog.Error("OTel shutdown", "error", err)
			}
		}()

		logger = sdk.NewLogger(os.Stdout)
		slog.SetDefault(logger)
		slog.Info("OTel enabled", "endpoint", cfg.OTelEndpoint)
	}

	r := chi.NewRouter()

	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.MaxBody(cfg.MaxBodyBytes))

	// When no OTel provider is registered, these calls are no-ops.
	m, err := metrics.NewMetrics(otel.GetMeterProvider().Meter("go-api-gateway"))
	if err != nil {
		slog.Error("failed to create metrics", "error", err)
		os.Exit(1)
	}
	r.Use(m.Middleware())
	r.Use(middleware.Tracing(
		otel.GetTracerProvider().Tracer("go-api-gateway"),
		otel.GetTextMapPropagator(),
	))
	r.Use(middleware.ConcurrencyLimit(cfg.MaxInFlight))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Required only when a service has auth: true, checked below per-service.
	var publicKey *rsa.PublicKey
	if cfg.JWTPublicKey != "" {
		var err error
		publicKey, err = loadPublicKey(cfg.JWTPublicKey)
		if err != nil {
			slog.Error("failed to load JWT public key", "error", err)
			os.Exit(1)
		}
	}

	redisClient, err := buildRedisClient(ctx, cfg)
	if err != nil {
		slog.Error("failed to build redis client", "error", err)
		os.Exit(1)
	}

	for _, svc := range cfg.Services {
		handler := buildServiceHandler(svc, cfg, ctx, logger)

		limiter, keyFunc, err := buildServiceLimiter(ctx, svc, cfg, redisClient)
		if err != nil {
			slog.Error("failed to build service rate limiter", "service", svc.Name, "error", err)
			os.Exit(1)
		}

		if svc.Auth && publicKey == nil {
			slog.Error("service requires auth but JWT_PUBLIC_KEY_PATH is not set", "service", svc.Name)
			os.Exit(1)
		}

		r.Route(svc.Prefix, func(r chi.Router) {
			if svc.Auth {
				r.Use(middleware.JWTAuth(publicKey))
			}
			if limiter != nil {
				r.Use(middleware.RateLimit(limiter, keyFunc, logger))
			}
			r.Mount("/", http.StripPrefix(svc.Prefix, handler))
		})

		slog.Info(
			"registered service",
			"name", svc.Name,
			"prefix", svc.Prefix,
			"upstreams", svc.Upstreams,
			"auth", svc.Auth,
			"rate_limited", limiter != nil,
		)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}

	go func() {
		slog.Info("gateway started", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown", "error", err)
	}
}

// buildRedisClient returns a connected *redis.Client when the global
// rate limiter is enabled with backend=redis, or nil otherwise. Pinging at
// startup surfaces a misconfigured backend before the first request.
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

// buildServiceLimiter returns the limiter and key function for one service.
// An override on the service wins over the global defaults; if neither
// applies, the returned limiter is nil (service has no rate limiting).
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
	ctx context.Context,
	logger *slog.Logger,
) http.Handler {
	checker := health.NewChecker(svc.Upstreams, cfg.HealthCheck.Interval, cfg.HealthCheck.Path)
	checker.Start(ctx)

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

	return proxy.NewHandler(lb, breakers, tc, logger)
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
