package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxy_ForwardsRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"service": "test-upstream"}`)
	}))
	defer upstream.Close()

	proxy, err := NewProxy(upstream.URL, slog.Default())
	if err != nil {
		t.Fatalf("NewProxy() error: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if body != `{"service": "test-upstream"}` {
		t.Errorf("body = %q, want %q", body, `{"service": "test-upstream"}`)
	}
}

func TestProxy_UpstreamDown(t *testing.T) {
	proxy, err := NewProxy("http://localhost:0", slog.Default())
	if err != nil {
		t.Fatalf("NewProxy() error: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if v := rec.Header().Get("Content-Type"); v != "application/json" {
		t.Errorf("content-type = %+v, want application/json", v)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Result().Body).Decode(&body); err != nil {
		t.Errorf("failed to decode JSON body: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("body doesn't contain 'error' key")
	}
}

func TestProxy_InvalidTarget(t *testing.T) {
	_, err := NewProxy("://bad", slog.Default())
	if err == nil {
		t.Errorf("error = nil, want non-nil error for invalid URL")
	}
}
