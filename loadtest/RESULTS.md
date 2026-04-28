# Load Test Results

Two workloads. A vegeta attack at a fixed rate, and a user-flow scenario with realistic session patterns.

To reproduce these runs, point the gateway container at `./loadtest/gateway.yaml` by swapping the volume mapping in `compose.yaml` (rate limiter is off there, which is what these numbers measure).

## Vegeta: 2400 req/s for 60s

```
./loadtest/vegeta/run.sh -max 2400 -start 2400 -dur 60s -conns 200
```

Vegeta splits evenly across three targets: `GET /catalog/products`, `GET /catalog/products/1`, and `GET /orders/orders` (authed).

**Client (vegeta):**

| Metric | Value |
|---|---|
| Requests / rate | 144,000 / 2400.01 req/s |
| Success | 100.00% (all 200) |
| Latency p50 / p95 / p99 / max | 0.66 ms / 1.31 ms / 2.94 ms / 32.6 ms |
| Errors | 0 |

**Server (gateway):**

| Metric | Value |
|---|---|
| Peak rate | 2400 req/s |
| 5xx count | 0 |
| p95 `/catalog/*` / `/orders/*` | 0.97 ms / 0.97 ms |
| p99 `/catalog/*` / `/orders/*` | 2.92 ms / 3.42 ms |
| Peak goroutines | 110 |
| Peak memory (stack + other) | ~22 MB |

Client and server latencies agree within a millisecond. No queuing. Goroutines stay near idle. This is the stable operating point on this host.

## User-flow: 200 users for 60s

```
go run ./loadtest/scenarios/ -users 200 -duration 60s -think 300ms
```

Each virtual user loops through `GET /catalog/products`, `GET /catalog/products/{id}`, `POST /orders/orders` (authed), then thinks 300 ms. Two catalog reads and one order write per cycle, so catalog gets roughly twice the traffic of orders. Each user has its own keep-alive client.

**Client:**

| Metric | Value |
|---|---|
| Requests / rate | 117,659 / ~1961 req/s |
| Success | 100.00% (200: 78,444, 201: 39,215) |
| Latency p50 | ~0.7 ms |
| Latency p95 | 2.7 - 8.8 ms (18 ms on first tick) |
| Latency p99 | 4 - 10 ms (64 ms on first tick) |
| Errors | 0 |

**Server (gateway):**

| Metric | Value |
|---|---|
| Peak rate | 1974 req/s |
| 5xx count | 0 |
| p95 `/catalog/*` / `/orders/*` | 0.97 ms / 9.4 ms |
| p99 `/catalog/*` / `/orders/*` | 2.5 ms / 10.0 ms |
| Peak goroutines | 580 |
| Peak memory (stack + other) | ~34 MB |

Client and server latencies line up closely, so users aren't queuing. First tick has a p95/p99 spike from first-request runtime costs (200 users firing at the same instant, Go's stdlib lazy-init, OTel histograms allocating buckets), then it settles. Catalog stays under 1 ms p95 because its reads are small. Orders runs an `INSERT ... RETURNING` with a price lookup, so it sits around 10 ms. Goroutine count peaks at 580, which tracks 200 in-flight users plus the gateway's proxy setup.

---

## How the tests are set up

### Host

Everything runs on one machine. Gateway, four upstream replicas, Postgres, Redis, OTel backend, and the load generator all share the same CPU, memory, and kernel network stack. The numbers above show the shape of gateway behavior, not an absolute ceiling. A production setup with the gateway on its own host and clients on the network would push further before hitting similar limits.

Specs: AMD Ryzen 7 5800H (8c/16t, up to 4.46 GHz), 31 GiB RAM, Linux 6.18, Docker 29.3.0. Ephemeral port range 32768-60999 (~28k ports), `tcp_fin_timeout=60`, `somaxconn=4096`. All services are in one `docker compose` project.

### Connections

Each upstream keeps a pool of 50 Postgres connections and warms them at startup. Without the warm pool, the first traffic spike triggers a burst of lazy dials, which overloads Docker's embedded DNS resolver. Four replicas times 50 is 200 connections against Postgres's 300-slot limit.

Gateway to upstream uses a tuned `http.Transport`: `MaxIdleConnsPerHost=200`, `MaxIdleConns=1000`, `IdleConnTimeout=90s`. Every proxied request reuses a keep-alive connection.

Client to gateway:
- vegeta uses `-conns 200`. Without it, vegeta opens a new TCP connection per request and the host exhausts ephemeral ports in seconds.
- user-flow gives each virtual user one `*http.Client` with default keep-alive, so one connection carries the whole session.

### Gateway features on during the test

Request ID, structured logging, 1 MB body limit, panic recovery, JWT validation on `/orders/*`, health checks, round-robin load balancer across the two replicas per service, circuit breaker (3 failures / 10s), global concurrency-limit middleware, OTel metrics, traces, logs. The rate limiter, response compression, and per-service caching are off, so these numbers are the raw proxy-path ceiling.

## Reading the numbers

### Dashboard vs client report

They rarely match exactly. Panels use rolling windows like `rate(...[1m])`, so a 60s test only fills the window at `T+60s` and starts decaying right after. Default panels show the latest scrape, not the peak. And "Total Requests" is a monotonic counter since gateway boot, not a per-test count.

There's also a real split between what the client measures and what the gateway measures. vegeta sees end-to-end latency: dial, request, response, plus any time spent queuing on the client for a free connection. The gateway sees only server-side latency, from accept to last byte written. Under pressure the client-side p99 can be an order of magnitude higher than the server-side p99. That gap is client queuing, not gateway slowness.

### Context-cancellation at the tail

A handful of `context canceled` lines on the gateway and upstreams at the end of a run is expected. When vegeta's attack window ends, in-flight requests have their client context cancelled. The gateway propagates the cancellation to the upstream, Postgres aborts the query, and the log line drops. That's the proxy behaving correctly.
