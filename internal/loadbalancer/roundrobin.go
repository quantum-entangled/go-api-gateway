package loadbalancer

import (
	"errors"
	"sync/atomic"
)

// ErrNoHealthyUpstreams is returned when all backends are unhealthy.
var ErrNoHealthyUpstreams = errors.New("no healthy upstreams available")

// HealthChecker reports whether a given URL is healthy.
// This is satisfied by *health.Checker without importing it.
type HealthChecker interface {
	IsHealthy(url string) bool
}

// RoundRobin distributes requests across healthy upstream URLs.
// It is safe for concurrent use.
type RoundRobin struct {
	urls    []string
	counter atomic.Uint64
	checker HealthChecker
}

// NewRoundRobin creates a RoundRobin load balancer for the given URLs.
func NewRoundRobin(urls []string, checker HealthChecker) *RoundRobin {
	return &RoundRobin{
		urls:    urls,
		checker: checker,
	}
}

// Next returns the next healthy upstream URL.
// Returns ErrNoHealthyUpstreams if all backends are down.
func (rr *RoundRobin) Next() (string, error) {
	attempts := uint64(len(rr.urls))

	for range attempts {
		i := rr.counter.Add(1) - 1
		url := rr.urls[i%attempts] // wraps around the URL slice on the border
		if rr.checker.IsHealthy(url) {
			return url, nil
		}
	}

	return "", ErrNoHealthyUpstreams
}
