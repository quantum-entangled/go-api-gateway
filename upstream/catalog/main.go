package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceCents  int       `json:"price_cents"`
	CreatedAt   time.Time `json:"created_at"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	db, err := openDB(os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /products", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(
			r.Context(),
			"SELECT id, name, description, price_cents, created_at FROM products",
		)
		if err != nil {
			logger.Error("failed to query the products", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		defer rows.Close()

		products := make([]product, 0)
		for rows.Next() {
			var p product
			err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.CreatedAt)
			if err != nil {
				logger.Error("failed to map the products", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			products = append(products, p)
		}

		if err := rows.Err(); err != nil {
			logger.Error("failed to iterate the rows", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string][]product{"products": products})
	})

	mux.HandleFunc("GET /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		var p product
		id := r.PathValue("id")

		err := db.QueryRowContext(
			r.Context(),
			"SELECT id, name, description, price_cents, created_at FROM products WHERE id = $1",
			id,
		).Scan(&p.ID, &p.Name, &p.Description, &p.PriceCents, &p.CreatedAt)
		if err == sql.ErrNoRows {
			logger.Info("product not found")
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "product not found"})
			return
		}
		if err != nil {
			logger.Error("failed to query or map the product", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]product{"product": p})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{Addr: ":" + port, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("catalog starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func writeJSON(w http.ResponseWriter, status int, msg any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(msg)
}
