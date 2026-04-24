package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go-api-gateway/internal/health"

	"github.com/stretchr/testify/assert"
)

func TestChecker_HealthyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	checker := health.NewChecker([]string{upstream.URL}, 50*time.Millisecond, "/healthz")
	checker.Start(ctx)

	assert.True(t, checker.IsHealthy(upstream.URL))
}

func TestChecker_UnhealthyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	checker := health.NewChecker([]string{upstream.URL}, 50*time.Millisecond, "/healthz")
	checker.Start(ctx)

	assert.False(t, checker.IsHealthy(upstream.URL))
}

func TestChecker_DownUpstream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	corruptedUrl := "http://localhost:0"
	checker := health.NewChecker([]string{corruptedUrl}, 50*time.Millisecond, "/healthz")
	checker.Start(ctx)

	assert.False(t, checker.IsHealthy(corruptedUrl))
}

func TestChecker_Recovery(t *testing.T) {
	var isHealthy atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	checker := health.NewChecker([]string{upstream.URL}, 10*time.Millisecond, "/healthz")
	isHealthy.Store(false)
	checker.Start(ctx)

	assert.False(t, checker.IsHealthy(upstream.URL))

	isHealthy.Store(true)
	time.Sleep(50 * time.Millisecond)
	assert.True(t, checker.IsHealthy(upstream.URL))
}

func TestChecker_UnknownURL(t *testing.T) {
	checker := health.NewChecker([]string{}, 50*time.Millisecond, "/healthz")

	assert.False(t, checker.IsHealthy("http://unregisteredurl:1234"))
}

func TestChecker_Probe_AnyHealthyReturnsNil(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	checker := health.NewChecker([]string{upstream.URL, "http://localhost:0"}, time.Second, "/healthz")
	err := checker.Probe(context.Background())

	assert.NoError(t, err)
	assert.True(t, checker.IsHealthy(upstream.URL))
}

func TestChecker_Probe_AllDownReturnsError(t *testing.T) {
	checker := health.NewChecker([]string{"http://localhost:0", "http://localhost:1"}, time.Second, "/healthz")
	err := checker.Probe(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no healthy upstreams")
}
