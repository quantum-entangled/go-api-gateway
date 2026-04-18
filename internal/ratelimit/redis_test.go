package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"go-api-gateway/internal/ratelimit"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func newRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	container, err := tcredis.Run(ctx, "redis:8.6-alpine")
	if err != nil {
		t.Skipf("skipping: cannot start redis container (is Docker running?): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	})

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: endpoint})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(ctx).Err())

	return client
}

func TestRedisLimiter_AllowsUpToBurst(t *testing.T) {
	client := newRedisClient(t)
	r := 1
	b := 5
	rl := ratelimit.NewRedisLimiter(client, float64(r), b)

	for i := range b {
		require.True(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be allowed", i+1)
	}

	assert.False(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be rejected", b+1)
}

func TestRedisLimiter_SeparateKeysAreIndependent(t *testing.T) {
	client := newRedisClient(t)
	r := 1
	b := 5
	rl := ratelimit.NewRedisLimiter(client, float64(r), b)

	for i := range b {
		require.True(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be allowed", i+1)
	}

	assert.True(t, mustAllowOrReject(t, rl, "client-2"))
}

func TestRedisLimiter_RefillsOverTime(t *testing.T) {
	client := newRedisClient(t)
	// 1 token per 10ms
	r := 100
	b := 5
	rl := ratelimit.NewRedisLimiter(client, float64(r), b)

	for i := range b {
		require.True(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be allowed", i+1)
	}

	time.Sleep(15 * time.Millisecond)
	assert.True(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be allowed", b+1)
}

// Two RedisLimiter instances against the same Redis must share the bucket.
// This is the defining property vs the in-memory limiter.
func TestRedisLimiter_SharedAcrossInstances(t *testing.T) {
	client := newRedisClient(t)
	r := 1
	b := 5
	rlFirst := ratelimit.NewRedisLimiter(client, float64(r), b)
	rlSecond := ratelimit.NewRedisLimiter(client, float64(r), b)

	for i := range b {
		var rl ratelimit.Limiter
		if i%2 == 0 {
			rl = rlFirst
		} else {
			rl = rlSecond
		}
		require.True(t, mustAllowOrReject(t, rl, "client-1"), "request %d should be allowed", i+1)
	}

	assert.False(t, mustAllowOrReject(t, rlFirst, "client-1"), "request %d should be rejected", b+1)
	assert.False(t, mustAllowOrReject(t, rlSecond, "client-1"), "request %d should be rejected", b+1)
}
