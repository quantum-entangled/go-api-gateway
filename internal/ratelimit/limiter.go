package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter decides whether a request identified by key should be allowed.
// Implementations may perform I/O.
type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// MemoryLimiter is an in-memory, per-key token-bucket limiter.
// Stale entries are cleaned up in the background.
type MemoryLimiter struct {
	entries         sync.Map
	r               rate.Limit
	b               int
	cleanupInterval time.Duration
	cleanupMaxIdle  time.Duration
}

// NewMemoryLimiter creates a MemoryLimiter that allows r requests per second
// with a burst size of b. Stale entries (not seen for cleanupMaxIdle) are
// removed every cleanupInterval. The prefix is accepted for parity with
// NewRedisLimiter and ignored: each instance owns its own map.
func NewMemoryLimiter(
	ctx context.Context,
	_ string,
	r rate.Limit,
	b int,
	cleanupInterval, cleanupMaxIdle time.Duration,
) *MemoryLimiter {
	ml := &MemoryLimiter{r: r, b: b, cleanupInterval: cleanupInterval, cleanupMaxIdle: cleanupMaxIdle}

	go func() {
		ticker := time.NewTicker(ml.cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ml.cleanup()
			case <-ctx.Done():
				return
			}
		}
	}()

	return ml
}

// Allow reports whether the request identified by key should be allowed.
// It ignores ctx, as MemoryLimiter performs no I/O.
func (ml *MemoryLimiter) Allow(_ context.Context, key string) (bool, error) {
	// Fast path: key already exists
	if value, ok := ml.entries.Load(key); ok {
		e := value.(*entry)
		ml.entries.Store(key, &entry{limiter: e.limiter, lastSeen: time.Now()})
		return e.limiter.Allow(), nil
	}

	// Slow path: first request from this key
	newEntry := &entry{limiter: rate.NewLimiter(ml.r, ml.b), lastSeen: time.Now()}
	actual, loaded := ml.entries.LoadOrStore(key, newEntry)
	if loaded {
		// Another goroutine was slightly faster
		e := actual.(*entry)
		return e.limiter.Allow(), nil
	}

	return newEntry.limiter.Allow(), nil
}

func (ml *MemoryLimiter) cleanup() {
	ml.entries.Range(func(key, value any) bool {
		e := value.(*entry)
		if time.Since(e.lastSeen) > ml.cleanupMaxIdle {
			ml.entries.Delete(key)
		}
		return true
	})
}

// Len returns the number of tracked keys. Only reliable in tests
// where concurrent access is controlled.
func (ml *MemoryLimiter) Len() int {
	var count int
	ml.entries.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}
