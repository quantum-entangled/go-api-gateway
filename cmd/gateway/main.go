package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"go-api-gateway/internal/circuitbreaker"
	"go-api-gateway/internal/config"
	"go-api-gateway/internal/health"
	"go-api-gateway/internal/loadbalancer"
	"go-api-gateway/internal/proxy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Each service gets its own set of upstreams, health checker, LB, and breakers.
	// For now each service has one instance; adding replicas is just a config change.
	handlerA := buildServiceHandler([]string{cfg.UpstreamAURL}, logger)
	handlerB := buildServiceHandler([]string{cfg.UpstreamBURL}, logger)

	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.Mount("/service-a", http.StripPrefix("/service-a", handlerA))
	r.Mount("/service-b", http.StripPrefix("/service-b", handlerB))

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("gateway started", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
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
