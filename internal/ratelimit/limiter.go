package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter decides whether a request identified by key should be allowed.
type Limiter interface {
	Allow(key string) bool
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// MemoryLimiter is an in-memory, per-key rate limiter backed by token buckets.
// Each unique key (e.g., client IP) gets its own bucket with the configured
// rate and burst size. Stale entries are cleaned up periodically.
type MemoryLimiter struct {
	entries         sync.Map
	r               rate.Limit
	b               int
	cleanupInterval time.Duration
	cleanupMaxIdle  time.Duration
}

// NewMemoryLimiter creates a MemoryLimiter that allows r requests per second
// with a burst size of b. Stale entries (not seen for cleanupMaxIdle) are
// removed every cleanupInterval.
func NewMemoryLimiter(
	ctx context.Context,
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
func (ml *MemoryLimiter) Allow(key string) bool {
	// Fast path: key already exists
	if value, ok := ml.entries.Load(key); ok {
		e := value.(*entry)
		ml.entries.Store(key, &entry{limiter: e.limiter, lastSeen: time.Now()})
		return e.limiter.Allow()
	}

	// Slow path: first request from this key
	newEntry := &entry{limiter: rate.NewLimiter(ml.r, ml.b), lastSeen: time.Now()}
	actual, loaded := ml.entries.LoadOrStore(key, newEntry)
	if loaded {
		// Another goroutine was slightly faster
		e := actual.(*entry)
		return e.limiter.Allow()
	}

	return newEntry.limiter.Allow()
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
