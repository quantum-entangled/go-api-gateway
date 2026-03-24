package proxy_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-api-gateway/internal/circuitbreaker"
	"go-api-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockLB struct {
	url string
	err error
}

func (m *mockLB) Next() (string, error) {
	return m.url, m.err
}

func newTestUpstream(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newBreakers(url string, maxFailures int) map[string]*circuitbreaker.Breaker {
	return map[string]*circuitbreaker.Breaker{
		url: circuitbreaker.NewBreaker(maxFailures, 10*time.Second),
	}
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var m map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&m))
	return m
}

func TestHandler_CircuitOpen(t *testing.T) {
	upstream := newTestUpstream(t, http.StatusOK, `{"ok": "true"}`)
	lb := &mockLB{url: upstream.URL}
	maxFailures := 3
	breakers := newBreakers(upstream.URL, maxFailures)

	for range maxFailures {
		breakers[upstream.URL].RecordFailure()
	}

	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)
	body := decodeJSON(t, rec)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, body["error"], "circuit breaker")
}

func TestHandler_AllUpstreamsDown(t *testing.T) {
	lb := &mockLB{url: "", err: errors.New("no healthy upstreams available")}
	h := proxy.NewHandler(lb, map[string]*circuitbreaker.Breaker{}, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)
	body := decodeJSON(t, rec)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, body["error"], "no healthy upstreams")
}

func TestHandler_ProxiesToHealthyUpstream(t *testing.T) {
	upstream := newTestUpstream(t, http.StatusOK, `{"ok": "true"}`)
	lb := &mockLB{url: upstream.URL}
	maxFailures := 3
	breakers := newBreakers(upstream.URL, maxFailures)

	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)
	body := decodeJSON(t, rec)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body["ok"], "true")
}

func TestHandler_RecordsSuccessOn2xx(t *testing.T) {
	upstream := newTestUpstream(t, http.StatusOK, `{"ok": "true"}`)
	lb := &mockLB{url: upstream.URL}
	maxFailures := 3
	breakers := newBreakers(upstream.URL, maxFailures)

	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)

	for range maxFailures - 1 {
		breakers[upstream.URL].RecordFailure()
	}

	assert.Equal(t, "closed", breakers[upstream.URL].State())
}

func TestHandler_RecordsFailureOn5xx(t *testing.T) {
	upstream := newTestUpstream(t, http.StatusInternalServerError, `{"ok": "false"}`)
	lb := &mockLB{url: upstream.URL}
	maxFailures := 3
	breakers := newBreakers(upstream.URL, maxFailures)

	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	for range maxFailures {
		h.ServeHTTP(rec, req)
	}

	assert.Equal(t, "open", breakers[upstream.URL].State())
}

func TestHandler_RecordsFailureOnConnectionError(t *testing.T) {
	unreachableURL := "http://localhost:0"
	lb := &mockLB{url: unreachableURL}
	maxFailures := 3
	breakers := newBreakers(unreachableURL, maxFailures)

	h := proxy.NewHandler(lb, breakers, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	for range maxFailures {
		h.ServeHTTP(rec, req)
	}

	assert.Equal(t, "open", breakers[unreachableURL].State())
}
