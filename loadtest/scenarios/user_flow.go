// user_flow simulates concurrent users doing realistic multi-step flows
// against the gateway: browse catalog -> view product -> place order.
//
// Unlike vegeta (which fires independent requests at a fixed rate), this
// test models actual user sessions with sequential steps and think time.
// It answers: "how many concurrent users can the gateway serve before
// latency degrades or errors appear?"
//
// The test prints per-interval stats so you can see trends over time.
// Compare with Grafana dashboards for server-side view.
//
// Usage:
//
//	go run ./loadtest/scenarios/ [flags...]
//
// Examples:
//
//	go run ./loadtest/scenarios/ -users 50 -duration 60s
//	go run ./loadtest/scenarios/ -users 100 -duration 120s -think 200ms
//	go run ./loadtest/scenarios/ -gateway http://other:8080 -key path/to/key
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go-api-gateway/loadtest"
)

type stats struct {
	mu        sync.Mutex
	latencies []time.Duration
	codes     map[int]int64
	errors    atomic.Int64
}

func (s *stats) record(code int, d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.codes[code]++
	s.mu.Unlock()
}

func (s *stats) recordError() {
	s.errors.Add(1)
}

// snapshot returns a copy of the current stats and resets them for the next interval.
func (s *stats) snapshot() (latencies []time.Duration, codes map[int]int64, errors int64) {
	s.mu.Lock()
	latencies = s.latencies
	codes = s.codes
	errors = s.errors.Load()
	s.latencies = nil
	s.codes = make(map[int]int64)
	s.errors.Store(0)
	s.mu.Unlock()
	return
}

func main() {
	gateway := flag.String("gateway", "http://localhost:8080", "gateway base URL")
	keyPath := flag.String("key", "example.key", "path to RSA private key")
	users := flag.Int("users", 10, "concurrent simulated users")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	think := flag.Duration("think", 300*time.Millisecond, "average think time between flows")
	interval := flag.Duration("interval", 5*time.Second, "stats reporting interval")
	flag.Parse()

	key, err := loadtest.LoadPrivateKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load key: %v\n", err)
		os.Exit(1)
	}

	token, err := loadtest.SignToken(key, "550e8400-e29b-41d4-a716-446655440000", []string{"admin"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sign token: %v\n", err)
		os.Exit(1)
	}

	transport := &http.Transport{
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 500,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	s := &stats{codes: make(map[int]int64)}

	fmt.Printf("Users: %d | Duration: %s | Think: %s | Target: %s\n\n", *users, *duration, *think, *gateway)
	fmt.Printf(
		"%-6s  %7s  %8s  %8s  %8s  %6s  %s\n",
		"TIME", "REQS", "P50", "P95", "P99", "ERRS",
		"STATUS CODES",
	)

	var wg sync.WaitGroup
	for range *users {
		wg.Go(func() {
			runUser(ctx, client, *gateway, token, s, *think)
		})
	}

	go reportLoop(ctx, s, *interval)

	wg.Wait()
	printInterval(s)
}

func runUser(ctx context.Context, client *http.Client, gateway, token string, s *stats, think time.Duration) {
	for {
		if ctx.Err() != nil {
			return
		}

		doReq(ctx, client, s, "GET", gateway+"/catalog/products", "", "")

		productID := rand.IntN(3) + 1
		doReq(ctx, client, s, "GET", fmt.Sprintf("%s/catalog/products/%d", gateway, productID), "", "")

		body, _ := json.Marshal(map[string]int{"product_id": productID, "quantity": rand.IntN(3) + 1})
		doReq(ctx, client, s, "POST", gateway+"/orders/orders", string(body), token)

		// Think time with jitter (50%-150% of configured value)
		jitter := time.Duration(float64(think) * (0.5 + rand.Float64()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
	}
}

func doReq(ctx context.Context, client *http.Client, s *stats, method, url, body, token string) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		s.recordError()
		return
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		if ctx.Err() == nil {
			s.recordError()
		}
		return
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	s.record(resp.StatusCode, elapsed)
}

func reportLoop(ctx context.Context, s *stats, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			printInterval(s)
		}
	}
}

func printInterval(s *stats) {
	latencies, codes, errs := s.snapshot()
	if len(latencies) == 0 && errs == 0 {
		return
	}

	slices.Sort(latencies)

	p50, p95, p99 := "-", "-", "-"
	if n := len(latencies); n > 0 {
		p50 = latencies[n*50/100].Truncate(100 * time.Microsecond).String()
		p95 = latencies[n*95/100].Truncate(100 * time.Microsecond).String()
		p99 = latencies[n*99/100].Truncate(100 * time.Microsecond).String()
	}

	codeStr := ""
	for code, count := range codes {
		if codeStr != "" {
			codeStr += " "
		}
		codeStr += fmt.Sprintf("%d:%d", code, count)
	}

	now := time.Now().Format("15:04")
	fmt.Printf("%-6s  %7d  %8s  %8s  %8s  %6d  %s\n",
		now, int64(len(latencies)), p50, p95, p99, errs, codeStr)
}
