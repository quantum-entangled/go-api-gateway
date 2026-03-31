package main

import (
	"context"
	"fmt"
	"log/slog"
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
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

	handlerA := buildServiceHandler([]string{cfg.UpstreamAURL}, logger)
	handlerB := buildServiceHandler([]string{cfg.UpstreamBURL}, logger)

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))

	// Observability middleware (active regardless of OTel endpoint -
	// when no real provider is registered, the OTel API calls are no-ops)
	m, err := metrics.NewMetrics(
		otel.GetMeterProvider().Meter("go-api-gateway"),
	)
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

	// Rate-limited service routes
	limiter := ratelimit.NewMemoryLimiter(ctx, rate.Limit(10), 20, time.Minute, 3*time.Minute)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimit(limiter, middleware.KeyByIP))
		r.Mount("/service-a", http.StripPrefix("/service-a", handlerA))
		r.Mount("/service-b", http.StripPrefix("/service-b", handlerB))
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

// buildServiceHandler creates a proxy.Handler for a set of upstream instances
// of the same service, with health checking, load balancing, and circuit breakers.
func buildServiceHandler(upstreams []string, logger *slog.Logger) http.Handler {
	checker := health.NewChecker(upstreams, 5*time.Second, "/healthz")
	checker.Start(context.Background())

	lb := loadbalancer.NewRoundRobin(upstreams, checker)

	breakers := make(map[string]*circuitbreaker.Breaker, len(upstreams))
	for _, u := range upstreams {
		breakers[u] = circuitbreaker.NewBreaker(3, 10*time.Second)
	}

	return proxy.NewHandler(lb, breakers, logger)
}
