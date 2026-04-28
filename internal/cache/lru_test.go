package cache_test

import (
	"testing"
	"time"

	"go-api-gateway/internal/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEntry(body string, ttl time.Duration) *cache.Entry {
	return &cache.Entry{
		Status:    200,
		Body:      []byte(body),
		ExpiresAt: time.Now().Add(ttl),
	}
}

func TestLRU_GetMissReturnsFalse(t *testing.T) {
	c := cache.NewLRU(10, 1024)
	_, ok := c.Get("absent")
	assert.False(t, ok)
}

func TestLRU_SetThenGet(t *testing.T) {
	c := cache.NewLRU(10, 1024)
	c.Set("k", newEntry("hello", time.Minute))

	got, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), got.Body)
}

func TestLRU_ExpiredEntryEvictedOnGet(t *testing.T) {
	c := cache.NewLRU(10, 1024)
	c.Set("k", newEntry("hello", -time.Second))

	_, ok := c.Get("k")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())
}

func TestLRU_EvictsOldestWhenAtEntryCap(t *testing.T) {
	c := cache.NewLRU(2, 1<<20)
	c.Set("a", newEntry("a", time.Minute))
	c.Set("b", newEntry("b", time.Minute))
	c.Set("c", newEntry("c", time.Minute))

	_, ok := c.Get("a")
	assert.False(t, ok)
	assert.Equal(t, 2, c.Len())
}

func TestLRU_EvictsOldestWhenOverByteCap(t *testing.T) {
	c := cache.NewLRU(100, 10)
	c.Set("a", newEntry("12345", time.Minute))
	c.Set("b", newEntry("67890", time.Minute))
	c.Set("c", newEntry("X", time.Minute))

	_, ok := c.Get("a")
	assert.False(t, ok)
	assert.LessOrEqual(t, c.Bytes(), 10)
}

func TestLRU_GetPromotesToFront(t *testing.T) {
	c := cache.NewLRU(2, 1<<20)
	c.Set("a", newEntry("a", time.Minute))
	c.Set("b", newEntry("b", time.Minute))

	// Touch "a" so "b" becomes the eviction target.
	_, _ = c.Get("a")
	c.Set("c", newEntry("c", time.Minute))

	_, okA := c.Get("a")
	_, okB := c.Get("b")
	assert.True(t, okA, "recently-touched entry must survive")
	assert.False(t, okB, "least-recently-used entry must be evicted")
}

func TestLRU_SetReplacesExistingKey(t *testing.T) {
	c := cache.NewLRU(10, 1<<20)
	c.Set("k", newEntry("first", time.Minute))
	c.Set("k", newEntry("second", time.Minute))

	got, ok := c.Get("k")
	require.True(t, ok)
	assert.Equal(t, []byte("second"), got.Body)
	assert.Equal(t, 1, c.Len(), "replacing must not add a duplicate entry")
	assert.Equal(t, len("second"), c.Bytes(), "byte count must reflect only the live entry")
}

func TestLRU_ExpiredEvictionUpdatesByteCount(t *testing.T) {
	c := cache.NewLRU(100, 1<<20)
	c.Set("k", newEntry("payload", -time.Second))
	require.Equal(t, len("payload"), c.Bytes())

	_, ok := c.Get("k")
	require.False(t, ok)
	assert.Equal(t, 0, c.Bytes())
}
