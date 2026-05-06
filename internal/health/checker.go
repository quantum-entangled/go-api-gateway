package health

import (
	"context"
	"fmt"
	"net/http"
	"slices"
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

// Probe runs one synchronous check pass and returns an error if no upstream
// is healthy. Used at startup so the gateway refuses to boot with all upstreams
// of a service dead.
func (c *Checker) Probe(ctx context.Context) error {
	c.checkAll(ctx)

	if slices.ContainsFunc(c.urls, c.IsHealthy) {
		return nil
	}
	return fmt.Errorf("no healthy upstreams after initial probe")
}

// Start begins periodic health checking in a background goroutine.
// Stops when the context is cancelled.
func (c *Checker) Start(ctx context.Context) {
	c.checkAll(ctx)

	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.checkAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *Checker) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, url := range c.urls {
		wg.Go(func() {
			c.check(ctx, url)
		})
	}
	wg.Wait()
}

func (c *Checker) check(ctx context.Context, url string) {
	req, err := http.NewRequestWithContext(ctx, "GET", url+c.path, nil)
	if err != nil {
		c.status.Store(url, false)
		return
	}

	res, err := c.client.Do(req)
	if err != nil {
		c.status.Store(url, false)
		return
	}
	defer func() { _ = res.Body.Close() }()

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
