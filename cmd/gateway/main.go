package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"go-api-gateway/internal/config"
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

	proxyA, err := proxy.NewProxy(cfg.UpstreamAURL, logger)
	if err != nil {
		slog.Error("failed to create proxy for upstream-a", "error", err)
		os.Exit(1)
	}

	proxyB, err := proxy.NewProxy(cfg.UpstreamBURL, logger)
	if err != nil {
		slog.Error("failed to create proxy for upstream-b", "error", err)
		os.Exit(1)
	}

	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// chi.Mount sets rctx.RoutePath for chi sub-routers but does not modify
	// r.URL.Path, so non-chi handlers like ReverseProxy still see the full
	// path. http.StripPrefix strips the mount prefix from r.URL.Path before
	// the proxy sees it.
	r.Mount("/service-a", http.StripPrefix("/service-a", proxyA))
	r.Mount("/service-b", http.StripPrefix("/service-b", proxyB))

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("gateway started", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
