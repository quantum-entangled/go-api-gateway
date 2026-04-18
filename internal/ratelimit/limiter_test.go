package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"go-api-gateway/internal/ratelimit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func mustAllowOrReject(t *testing.T, ml ratelimit.Limiter, key string) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	allowed, err := ml.Allow(ctx, key)
	require.NoError(t, err)

	return allowed
}

func TestMemoryLimiter_AllowsUpToBurst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r := 1
	b := 5
	ml := ratelimit.NewMemoryLimiter(ctx, rate.Limit(r), b, time.Minute, time.Minute)

	for i := range b {
		require.True(t, mustAllowOrReject(t, ml, "client-1"), "request %d should be allowed", i+1)
	}

	assert.False(t, mustAllowOrReject(t, ml, "client-1"), "request %d should be rejected", b+1)
}

func TestMemoryLimiter_SeparateKeysAreIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r := 1
	b := 5
	ml := ratelimit.NewMemoryLimiter(ctx, rate.Limit(r), b, time.Minute, time.Minute)

	for i := range b {
		require.True(t, mustAllowOrReject(t, ml, "client-1"), "request %d should be allowed", i+1)
	}

	assert.True(t, mustAllowOrReject(t, ml, "client-2"))
}

func TestMemoryLimiter_RefillsOverTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// 1 token per 10ms
	r := 100
	b := 5
	ml := ratelimit.NewMemoryLimiter(ctx, rate.Limit(r), b, 1*time.Minute, 1*time.Minute)

	for i := range b {
		require.True(t, mustAllowOrReject(t, ml, "client-1"), "request %d should be allowed", i+1)
	}

	time.Sleep(15 * time.Millisecond)
	assert.True(t, mustAllowOrReject(t, ml, "client-1"), "request %d should be allowed", b+1)
}

func TestMemoryLimiter_CleanupRemovesStaleEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r := 1
	b := 5
	ml := ratelimit.NewMemoryLimiter(ctx, rate.Limit(r), b, 5*time.Millisecond, 5*time.Millisecond)

	require.True(t, mustAllowOrReject(t, ml, "client-1"), "request 1 should be allowed")
	assert.Equal(t, 1, ml.Len(), "should have 1 tracked key")
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 0, ml.Len(), "stale entry should be cleaned up")
}
