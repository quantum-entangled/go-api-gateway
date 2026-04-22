package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-api-gateway/internal/config"
	gatewayotel "go-api-gateway/internal/otel"
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

	router, err := buildRouter(ctx, cfg, logger)
	if err != nil {
		slog.Error("failed to build router", "error", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
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
