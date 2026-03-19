package health

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Checker periodically polls upstream URLs and tracks which ones are healthy.
// The load balancer uses IsHealthy to skip dead backends.
type Checker struct {
	urls     []string
	client   *http.Client
	status   sync.Map
	interval time.Duration
	path     string
}

// NewChecker creates a Checker for the given upstream URLs.
// It does NOT start polling - call Start for that.
func NewChecker(urls []string, interval time.Duration, path string) *Checker {
	return &Checker{
		urls:     urls,
		client:   &http.Client{Timeout: 2 * time.Second},
		interval: interval,
		path:     path,
	}
}

// Start begins periodic health checking in a background goroutine.
// It polls all upstreams concurrently on each tick.
// Stops when the context is cancelled.
func (c *Checker) Start(ctx context.Context) {
	c.checkAll()

	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.checkAll()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// checkAll pings every upstream concurrently and updates their health status.
func (c *Checker) checkAll() {
	var wg sync.WaitGroup
	for _, url := range c.urls {
		wg.Go(func() {
			c.check(url)
		})
	}
	wg.Wait()
}

// check pings a single upstream and updates its health status.
func (c *Checker) check(url string) {
	res, err := c.client.Get(url + c.path)
	if err != nil {
		c.status.Store(url, false)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		c.status.Store(url, false)
		return
	}
	c.status.Store(url, true)
}

// IsHealthy reports whether the given upstream URL is currently healthy.
func (c *Checker) IsHealthy(url string) bool {
	isHealthy, ok := c.status.Load(url)
	if !ok {
		return false
	}
	return isHealthy.(bool)
}
