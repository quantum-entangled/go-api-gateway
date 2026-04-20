package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"go-api-gateway/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyLimit_UnderCapAllAllowed(t *testing.T) {
	limit := 5
	handler := middleware.ConcurrencyLimit(limit)(okHandler())

	var wg sync.WaitGroup
	for range limit {
		wg.Go(func() {
			for range limit {
				req := httptest.NewRequest("GET", "/", nil)
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				assert.Equal(t, http.StatusOK, res.Code)
			}
		})
	}
	wg.Wait()
}

func TestConcurrencyLimit_OverCapSheds(t *testing.T) {
	var called atomic.Int64
	limit := 2
	started := make(chan struct{}, limit)
	done := make(chan struct{})

	blockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		started <- struct{}{}
		<-done
	})
	handler := middleware.ConcurrencyLimit(limit)(blockHandler)

	var wg sync.WaitGroup
	for range limit {
		wg.Go(func() {
			req := httptest.NewRequest("GET", "/", nil)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
		})
	}

	for range limit {
		<-started
	}
	assert.Equal(t, int64(limit), called.Load())

	req := httptest.NewRequest("GET", "/", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusServiceUnavailable, res.Code)
	assert.Equal(t, "application/json", res.Header().Get("Content-Type"))
	assert.Equal(t, "1", res.Header().Get("Retry-After"))

	var body map[string]string
	err := json.NewDecoder(res.Body).Decode(&body)

	require.NoError(t, err)
	assert.Equal(t, "server overloaded", body["error"])
	assert.Equal(t, int64(limit), called.Load())

	close(done)
	wg.Wait()
}

func TestConcurrencyLimit_SlotReleasedAfterHandler(t *testing.T) {
	started := make(chan struct{}, 1)
	done := make(chan struct{}, 1)

	blockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		started <- struct{}{}
		<-done
	})
	handler := middleware.ConcurrencyLimit(1)(blockHandler)
	reqFirst := httptest.NewRequest("GET", "/", nil)
	resFirst := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Go(func() {
		handler.ServeHTTP(resFirst, reqFirst)
	})

	<-started
	assert.Equal(t, http.StatusOK, resFirst.Code)

	close(done)
	wg.Wait()

	reqSecond := httptest.NewRequest("GET", "/", nil)
	resSecond := httptest.NewRecorder()

	handler.ServeHTTP(resSecond, reqSecond)
	assert.Equal(t, http.StatusOK, resSecond.Code)
}

func TestConcurrencyLimit_SlotReleasedOnPanic(t *testing.T) {
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Panic") == "true" {
			panic("panic")
		}
		w.WriteHeader(http.StatusOK)
	})
	recoverHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
	handler := middleware.ConcurrencyLimit(1)(recoverHandler(panicHandler))
	reqFirst := httptest.NewRequest("GET", "/", nil)
	reqFirst.Header.Set("Panic", "true")
	resFirst := httptest.NewRecorder()

	handler.ServeHTTP(resFirst, reqFirst)
	assert.Equal(t, http.StatusInternalServerError, resFirst.Code)

	reqSecond := httptest.NewRequest("GET", "/", nil)
	resSecond := httptest.NewRecorder()

	handler.ServeHTTP(resSecond, reqSecond)
	assert.Equal(t, http.StatusOK, resSecond.Code)
}

func TestConcurrencyLimit_DisabledWhenLimitZero(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := middleware.ConcurrencyLimit(tt.limit)(okHandler())

			var wg sync.WaitGroup
			for range 50 {
				wg.Go(func() {
					req := httptest.NewRequest("GET", "/", nil)
					res := httptest.NewRecorder()
					handler.ServeHTTP(res, req)
					assert.Equal(t, http.StatusOK, res.Code)
				})
			}
			wg.Wait()
		})
	}
}
