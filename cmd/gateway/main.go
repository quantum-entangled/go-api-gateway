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

	// OTel setup (optional - disabled when endpoint is empty)
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

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))

	// Observability middleware (active regardless of OTel endpoint -
	// when no real provider is registered, the OTel API calls are no-ops)
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

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Load RSA public key for JWT validation (optional - required when any service has auth: true)
	var publicKey *rsa.PublicKey
	if cfg.JWTPublicKey != "" {
		var err error
		publicKey, err = loadPublicKey(cfg.JWTPublicKey)
		if err != nil {
			slog.Error("failed to load JWT public key", "error", err)
			os.Exit(1)
		}
	}

	// Service routes
	r.Group(func(r chi.Router) {
		if cfg.RateLimit.Enabled {
			limiter := ratelimit.NewMemoryLimiter(
				ctx,
				rate.Limit(cfg.RateLimit.Rate),
				cfg.RateLimit.Burst,
				cfg.RateLimit.CleanupInterval,
				cfg.RateLimit.CleanupMaxIdle,
			)
			r.Use(middleware.RateLimit(limiter, middleware.KeyByIP))
		}

		for _, svc := range cfg.Services {
			handler := buildServiceHandler(svc, cfg, ctx, logger)

			if svc.Auth {
				if publicKey == nil {
					slog.Error("service requires auth but JWT_PUBLIC_KEY_PATH is not set", "service", svc.Name)
					os.Exit(1)
				}
				r.Route(svc.Prefix, func(r chi.Router) {
					r.Use(middleware.JWTAuth(publicKey))
					r.Mount("/", http.StripPrefix(svc.Prefix, handler))
				})
			} else {
				r.Mount(svc.Prefix, http.StripPrefix(svc.Prefix, handler))
			}

			slog.Info(
				"registered service",
				"name", svc.Name,
				"prefix", svc.Prefix,
				"upstreams", svc.Upstreams,
				"auth", svc.Auth,
			)
		}
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

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

	return proxy.NewHandler(lb, breakers, logger)
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
