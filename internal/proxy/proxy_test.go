package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newBreakers(url string, maxFailures int) map[string]*circuitbreaker.Breaker {
	return map[string]*circuitbreaker.Breaker{
		url: circuitbreaker.NewBreaker(maxFailures, 10*time.Second),
	}
}

var testTransport = proxy.TransportConfig{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	DialTimeout:         5 * time.Second,
	TLSHandshakeTimeout: 5 * time.Second,
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

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)
	body := decodeJSON(t, rec)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, body["error"], "circuit breaker")
}

func TestHandler_AllUpstreamsDown(t *testing.T) {
	lb := &mockLB{url: "", err: errors.New("no healthy upstreams available")}
	h := proxy.NewHandler(lb, map[string]*circuitbreaker.Breaker{}, testTransport, slog.Default())
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

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
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

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
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

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	var rec *httptest.ResponseRecorder
	req := httptest.NewRequest("GET", "/", nil)

	for range maxFailures {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	assert.Equal(t, "open", breakers[upstream.URL].State())
}

func TestHandler_MaxBytesErrorReturns413(t *testing.T) {
	upstream := newTestUpstream(t, http.StatusOK, `{"ok":"true"}`)
	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 2048)))
	rec := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(rec, req.Body, 10)
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	body := decodeJSON(t, rec)
	assert.Equal(t, "request body too large", body["error"])
	assert.Equal(t, "closed", breakers[upstream.URL].State(), "413 must not trip the circuit breaker")
}

func TestHandler_RecordsFailureOnConnectionError(t *testing.T) {
	unreachableURL := "http://localhost:0"
	lb := &mockLB{url: unreachableURL}
	maxFailures := 3
	breakers := newBreakers(unreachableURL, maxFailures)

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	var rec *httptest.ResponseRecorder
	req := httptest.NewRequest("GET", "/", nil)

	for range maxFailures {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	assert.Equal(t, "open", breakers[unreachableURL].State())
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	body := decodeJSON(t, rec)
	assert.Equal(t, "upstream unavailable", body["error"])
}

func TestHandler_UpstreamTimeoutReturns504(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)

	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	body := decodeJSON(t, rec)
	assert.Equal(t, "upstream timeout", body["error"])
}
