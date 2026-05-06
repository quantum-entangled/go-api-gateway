package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-playground/validator/v10"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type order struct {
	ID         int       `json:"id"`
	UserID     string    `json:"user_id"`
	ProductID  int       `json:"product_id"`
	Quantity   int       `json:"quantity"`
	TotalCents int       `json:"total_cents"`
	CreatedAt  time.Time `json:"created_at"`
}

type orderCreate struct {
	ProductID int `json:"product_id" validate:"required,gt=0"`
	Quantity  int `json:"quantity" validate:"required,gte=1,lte=100"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	db, err := openDB(os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	mux := http.NewServeMux()
	validate := validator.New()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(
			r.Context(),
			`SELECT id, user_id, product_id, quantity, total_cents, created_at
			FROM orders
			WHERE user_id = $1`,
			r.Header.Get("X-User"),
		)
		if err != nil {
			logger.Error("failed to query the orders", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		defer func() { _ = rows.Close() }()

		orders := make([]order, 0)
		for rows.Next() {
			var o order
			err := rows.Scan(&o.ID, &o.UserID, &o.ProductID, &o.Quantity, &o.TotalCents, &o.CreatedAt)
			if err != nil {
				logger.Error("failed to map the orders", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			orders = append(orders, o)
		}

		if err := rows.Err(); err != nil {
			logger.Error("failed to iterate the orders", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string][]order{"orders": orders})
	})

	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var payload orderCreate
		err := json.NewDecoder(r.Body).Decode(&payload)
		if err != nil {
			logger.Error("failed to extract the request body", "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}

		if err := validate.Struct(payload); err != nil {
			var ve validator.ValidationErrors
			if errors.As(err, &ve) {
				logger.Error("validation failed", "field", ve[0].Field(), "tag", ve[0].Tag())
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("invalid field %s: %s", ve[0].Field(), ve[0].Tag()),
				})
				return
			}

			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}

		var (
			id         int
			totalCents int
		)
		err = db.QueryRowContext(
			r.Context(),
			`INSERT INTO orders (user_id, product_id, quantity, total_cents)
			SELECT $1, $2, $3, p.price_cents * $3
			FROM products AS p
			WHERE p.id = $2
			RETURNING id, total_cents`,
			r.Header.Get("X-User"), payload.ProductID, payload.Quantity,
		).Scan(&id, &totalCents)
		if err == sql.ErrNoRows {
			logger.Info("product not found")
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product not found"})
			return
		}
		if err != nil {
			logger.Error("failed to insert the order", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]int{"id": id, "total_cents": totalCents})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{Addr: ":" + port, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("orders starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown", "error", err)
	}
}

func openDB(dsn string) (*sql.DB, error) {
	maxConns := 50
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid DB_MAX_CONNS %q: %w", v, err)
		}
		maxConns = n
	}
	connMaxLifetime := 30 * time.Minute
	if v := os.Getenv("DB_CONN_MAX_LIFETIME"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid DB_CONN_MAX_LIFETIME %q: %w", v, err)
		}
		connMaxLifetime = d
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := warmPool(ctx, db, maxConns); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// warmPool opens n connections in parallel and holds them briefly so they all
// stay in the idle pool. Without this, the pool grows lazily under load - and
// under a burst, many goroutines race to open new connections at once, which
// can overwhelm Docker's embedded DNS resolver.
func warmPool(ctx context.Context, db *sql.DB, n int) error {
	conns := make([]*sql.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for range n {
		c, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		conns = append(conns, c)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, msg any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(msg)
}
