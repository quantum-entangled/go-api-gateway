# Go API Gateway

[![ci](https://img.shields.io/github/actions/workflow/status/quantum-entangled/go-api-gateway/ci.yaml?branch=main&label=ci&logo=github)](https://github.com/quantum-entangled/go-api-gateway/actions/workflows/ci.yaml)
[![release](https://img.shields.io/github/v/release/quantum-entangled/go-api-gateway?label=release&logo=github&color=blue)](https://github.com/quantum-entangled/go-api-gateway/releases)
[![go report card](https://goreportcard.com/badge/github.com/quantum-entangled/go-api-gateway)](https://goreportcard.com/report/github.com/quantum-entangled/go-api-gateway)
[![go version](https://img.shields.io/github/go-mod/go-version/quantum-entangled/go-api-gateway?label=go)](go.mod)

A self-contained API gateway in Go. It sits in front of HTTP services and handles the things you don't want each service to reimplement: auth checks, rate limiting, caching, compression, load balancing, health checks, circuit breaking, concurrency caps, and request size limits.

## Features

- Reverse proxy with health-aware round-robin load balancing across upstream replicas
- Per-upstream circuit breaker with a closed -> open -> half-open FSM
- Concurrent upstream health checks, with unhealthy upstreams taken out of rotation and exposed as a per-upstream metric
- JWT verification (RS256, algorithm-pinned) per service, with role extraction into request context and optional any-of role allowlist
- Rate limiting, in-memory or Redis-backed token bucket, keyed by IP or JWT subject, global or per service
- Response cache with LRU eviction, TTL, Vary-aware keys, ETag/304 support, and singleflight to prevent stampedes
- gzip compression with a configurable size threshold
- Per-request body size limit, header size limit, and global in-flight concurrency cap with a 503 fallback
- Request ID injection and propagation
- Panic recovery middleware
- Structured logging via `log/slog`
- OpenTelemetry traces, metrics, and logs over OTLP/HTTP
- Graceful shutdown on SIGTERM/SIGINT

## How to use

Two ways, both need Docker.

To try it end-to-end with dummy upstream services, see [Example stack](#example-stack).

To drop it in front of your own services, pull the image:

```
docker pull ghcr.io/quantum-entangled/go-api-gateway:latest
```

Then run it, mounting your config (and the JWT public key, if any service uses auth):

```
docker run --rm -p 8080:8080 \
  -v $(pwd)/gateway.yaml:/etc/gateway/gateway.yaml:ro \
  -v $(pwd)/jwt.pem:/etc/gateway/jwt.pem:ro \
  -e JWT_PUBLIC_KEY_PATH=/etc/gateway/jwt.pem \
  ghcr.io/quantum-entangled/go-api-gateway:latest \
  -config /etc/gateway/gateway.yaml
```

The image is small and distroless. See [Configuration](#configuration) for `gateway.yaml` and env vars.

## Configuration

Two layers:

- `gateway.yaml`: services and middleware settings. Passed in as `-config /path/to/gateway.yaml`.
- Env vars: infra secrets and the OTel endpoint.

If a feature key is omitted, the feature is off. So a minimal config is one service entry with `name`, `prefix`, and `upstreams`. Anything else has a sensible default.

The gateway listens plaintext HTTP. Terminate TLS upstream of it. Outbound to upstreams and OTel can use HTTPS.

### YAML (gateway.yaml)

| Key | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `port` | int | no | `8080` | Listen port. |
| `max_body_bytes` | int | no | `1048576` | Per-request body limit. |
| `max_header_bytes` | int | no | `32768` | Header size limit on `http.Server`. |
| `max_in_flight` | int | no | `0` (off) | Global concurrency cap. Excess gets 503. |
| `compression.enabled` | bool | no | `false` | gzip responses on service routes. |
| `compression.min_bytes` | int | no | `1024` | Skip compression for bodies smaller than this. |
| `rate_limit.backend` | string | no | `memory` | `memory` or `redis`. |
| `rate_limit.rate` | float | yes if block set | - | Tokens per second. |
| `rate_limit.burst` | int | yes if block set | - | Bucket size. |
| `rate_limit.cleanup_interval` | duration | no | `1m` | How often the in-memory limiter sweeps idle keys. |
| `rate_limit.cleanup_max_idle` | duration | no | `3m` | Idle threshold before a key is dropped. |
| `rate_limit.redis.addr` | string | yes if backend=redis | - | `host:port`. |
| `rate_limit.redis.pool_size` | int | no | go-redis default | Connection pool size. |
| `rate_limit.redis.dial_timeout` | duration | no | go-redis default | TCP dial timeout. |
| `rate_limit.redis.read_timeout` | duration | no | go-redis default | Read timeout. |
| `rate_limit.redis.write_timeout` | duration | no | go-redis default | Write timeout. |
| `rate_limit.redis.tls` | bool | no | `false` | Connect to Redis over TLS 1.2+. |
| `health_check.interval` | duration | no | `5s` | How often each upstream is probed. |
| `health_check.path` | string | no | `/healthz` | Probe path. |
| `circuit_breaker.max_failures` | int | yes if block set | - | Consecutive failures that trip the breaker. |
| `circuit_breaker.timeout` | duration | yes if block set | - | Open -> half-open delay. |
| `transport.max_idle_conns` | int | no | `1000` | `http.Transport.MaxIdleConns`. |
| `transport.max_idle_conns_per_host` | int | no | `200` | `http.Transport.MaxIdleConnsPerHost`. |
| `transport.idle_conn_timeout` | duration | no | `90s` | Idle keep-alive timeout. |
| `transport.dial_timeout` | duration | no | `5s` | TCP dial timeout. |
| `transport.tls_handshake_timeout` | duration | no | `5s` | TLS handshake timeout. |
| `services[].name` | string | yes | - | Service identifier (used in logs and metrics). Must be unique. |
| `services[].prefix` | string | yes | - | Path prefix the gateway routes on, e.g. `/catalog`. Stripped before forwarding. Must be unique. |
| `services[].upstreams` | []string | yes | - | Backend URLs. Round-robin across healthy ones. |
| `services[].auth` | bool | no | `false` | Require a valid JWT for this service. |
| `services[].required_roles` | []string | no | - | Allowed JWT roles (any-of). Requires `auth: true`. |
| `services[].rate_limit.rate` | float | yes if block set | - | Per-service override. |
| `services[].rate_limit.burst` | int | yes if block set | - | Per-service override. |
| `services[].rate_limit.key_by` | string | no | `ip` | `ip` or `jwt_sub`. `jwt_sub` requires `auth: true`. |
| `services[].cache.ttl` | duration | no | `60s` | Default cache TTL. Upstream `Cache-Control: max-age` overrides it. |
| `services[].cache.max_entries` | int | no | `1024` | LRU entry cap. |
| `services[].cache.max_bytes` | int | no | `16777216` | LRU byte cap. |

### Required environment variables

| Name | Required | Meaning |
|---|---|---|
| `JWT_PUBLIC_KEY_PATH` | yes if any service has `auth: true` | Path to the RSA public key in PEM/PKIX. |
| `REDIS_PASSWORD` | yes if `rate_limit.backend: redis` | Forwarded to the Redis client. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | OTLP endpoint URL, e.g. `http(s)://otel-lgtm:4318`. Empty disables OTel. |

## Example stack

The repo ships a working compose stack. Upstreams run as two replicas each for load balancing:

- `catalog`, `catalog-2`: a small read-mostly HTTP service backed by Postgres
- `orders`, `orders-2`: a write-mostly HTTP service backed by Postgres, requires JWT auth
- `postgres`: shared Postgres instance, separate goose migration tables per service
- `migrate`: one-shot job that runs both services' migrations, then exits
- `redis`: backs the gateway's rate limiter
- `otel-lgtm`: `grafana/otel-lgtm` (OTel collector + Tempo + Mimir/Prometheus + Loki + Grafana, all in one image)
- `gateway`: built from this repo, mounting `gateway.yaml` and the dev public key

### Required env and dev keys

The compose stack reads `.env.example` via `--env-file` (see the `Makefile`). It carries credentials and service setup.

JWT verification needs an RSA keypair. The gateway loads `example.pem` (public). `example.key` (private) is used by anything signing tokens, like `loadtest/cmd/gentoken`.

> [!WARNING]
> `.env.example`, `example.key`, and `example.pem` are committed for convenience so the example boots out of the box. They're examples only. Don't copy them as-is and don't put real secrets in them.

### Running

```
make infra-up
```

```
make infra-down
```

The gateway is at `http://localhost:${GATEWAY_PORT}`. Sanity check:

```
curl http://localhost:8080/catalog/products
```

```
TOKEN=$(go run ./loadtest/cmd/gentoken -key example.key)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/orders/orders
```

### Observability

- Grafana: `http://localhost:${GRAFANA_PORT}`, login `admin` / `admin`.
- Default home dashboard: `grafana/dashboards/gateway-overview.json` (provisioned at boot). Covers traffic, latency, resource use, and per-upstream health, with logs and traces linked.
- OTLP receivers: `${OTEL_GRPC_PORT}` (4317), `${OTEL_HTTP_PORT}` (4318). The gateway uses HTTP.
- Different backend? Point `OTEL_EXPORTER_OTLP_ENDPOINT` at its OTLP/HTTP URL.

## Load testing

Two generators in `loadtest/`, both expect the gateway at `http://localhost:8080`:

- `loadtest/vegeta/run.sh`: vegeta at a fixed or ramping rate against `/catalog/products`, `/catalog/products/1`, `/orders/orders` (authed). Requires [vegeta](https://github.com/tsenart/vegeta) on `PATH`, or pass its location via `VEGETA=/path/to/vegeta`. Results in `loadtest/vegeta/results/`.
- `loadtest/scenarios/user_flow.go`: N concurrent users doing browse-and-order sessions with think time.

Recent run on a single host (Ryzen 7 5800H): vegeta at 2400 req/s for 60s, 100% success, p99 under 3 ms server-side. User-flow at 200 concurrent users hit ~1960 req/s, p99 around 10 ms on the orders write path. Full numbers, host setup, and reproduction steps in `loadtest/RESULTS.md`.

## Tests

`make test` runs everything under `-race`:

- Unit tests sit next to the packages they test.
- E2E tests in `cmd/gateway/e2e_test.go` drive the middleware chain over a real listener against a dummy upstream.
- Integration tests in `internal/ratelimit/` hit a real Redis via testcontainers (requires Docker).
