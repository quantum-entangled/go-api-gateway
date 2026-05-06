package proxy_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"go-api-gateway/internal/middleware"
	"go-api-gateway/internal/proxy"
)

var testKey *rsa.PrivateKey

func init() {
	var err error
	testKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("failed to generate test RSA key: " + err.Error())
	}
}

func signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	s, err := token.SignedString(testKey)
	require.NoError(t, err)
	return s
}

func newTestTracing(t *testing.T) trace.Tracer {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider.Tracer("test")
}

func TestInjectTraceContext_AddsTraceparentHeader(t *testing.T) {
	tracer := newTestTracing(t)
	ctx, span := tracer.Start(context.Background(), "test")
	defer span.End()

	originalPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(originalPropagator) })

	var header string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)
	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	h.ServeHTTP(rec, req)

	assert.NotEmpty(t, header)
}

func TestInjectUserHeaders_SetsHeadersFromJWT(t *testing.T) {
	tokenStr := signToken(t, jwt.MapClaims{
		"sub":   "user1",
		"roles": []string{"admin", "vip"},
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	var gotUser, gotRoles string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User")
		gotRoles = r.Header.Get("X-Roles")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)
	proxyHandler := proxy.NewHandler(lb, breakers, testTransport, slog.Default())

	// Wrap with JWTAuth so claims land in the context.
	handler := middleware.JWTAuth(&testKey.PublicKey)(proxyHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "user1", gotUser)
	assert.Equal(t, "admin,vip", gotRoles)
}

func TestInjectUserHeaders_NoHeadersWithoutAuth(t *testing.T) {
	var gotUser, gotRoles string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User")
		gotRoles = r.Header.Get("X-Roles")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	lb := &mockLB{url: upstream.URL}
	breakers := newBreakers(upstream.URL, 3)
	h := proxy.NewHandler(lb, breakers, testTransport, slog.Default())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, gotUser)
	assert.Empty(t, gotRoles)
}
