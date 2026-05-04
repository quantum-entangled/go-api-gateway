package otel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

var testIntervals = Intervals{
	MetricInterval:    10 * time.Second,
	TraceBatchTimeout: 5 * time.Second,
	LogBatchTimeout:   1 * time.Second,
}

// fakeOTLP returns an httptest.Server that accepts OTLP HTTP exports without
// inspecting them. Setup needs a reachable host:port even if no batches flush.
func fakeOTLP(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

func newTestSDK(t *testing.T) *SDK {
	t.Helper()
	ctx := context.Background()
	sdk, err := Setup(ctx, fakeOTLP(t), testIntervals)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sdk.Shutdown(ctx) })
	return sdk
}

func TestSetup_ReturnsUsableSDK(t *testing.T) {
	sdk := newTestSDK(t)

	assert.NotNil(t, sdk.TracerProvider)
	assert.NotNil(t, sdk.MeterProvider)
	assert.NotNil(t, sdk.LoggerProvider)
}

func TestSetup_RegistersGlobalProvidersAndPropagator(t *testing.T) {
	sdk := newTestSDK(t)
	assert.Same(t, sdk.TracerProvider, otel.GetTracerProvider())

	_, isTC := otel.GetTextMapPropagator().(propagation.TraceContext)
	assert.True(t, isTC, "global propagator must be W3C TraceContext")
}

func TestShutdown_IsSafeToCallTwice(t *testing.T) {
	ctx := context.Background()
	sdk, err := Setup(ctx, fakeOTLP(t), testIntervals)
	require.NoError(t, err)

	require.NoError(t, sdk.Shutdown(ctx))
	// Providers may return an error after being shut down.
	// We accept that but require no crash.
	_ = sdk.Shutdown(ctx)
}

func TestNewLogger_WritesJSONToProvidedWriter(t *testing.T) {
	sdk := newTestSDK(t)

	var buf bytes.Buffer
	logger := sdk.NewLogger(&buf)
	logger.Info("hello", "user", "alice")

	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &parsed))
	assert.Equal(t, "hello", parsed["msg"])
	assert.Equal(t, "alice", parsed["user"])
}

func TestNewLogger_LevelFiltering(t *testing.T) {
	sdk := newTestSDK(t)
	var buf bytes.Buffer
	logger := sdk.NewLogger(&buf)

	// Default JSONHandler level is Info, so Debug must not appear.
	logger.Debug("hidden")
	assert.Empty(t, buf.String())

	logger.Warn("visible")
	assert.Contains(t, buf.String(), "visible")
}
