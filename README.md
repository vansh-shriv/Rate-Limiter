# Distributed Rate Limiter as a Service

A drop-in API gateway and decision API that rate-limits traffic per tenant, using **token bucket**, **sliding window**
or **leaky bucket** algorithms. Every decision is one atomic **Redis Lua script**, so any number of stateless gateway
instances share exact limits. Comes with an admin API, per-tenant API keys, Prometheus metrics and a Grafana dashboard.

**Performance, honestly:** measured full-stack on a shared 8-core laptop under WSL2, it sustains **~10k req/s at
p99 ≈ 3–4 ms** (5k req/s at p99 ≈ 2 ms). The original goal of **20k req/s at p99 < 5 ms was not met on that hardware** and
is not claimed. Method, profile and how to re-measure on proper hardware: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

## Features
- **Three algorithms**, selectable per tenant: token bucket (burst-friendly), sliding window log (exact), leaky bucket (shaper that spaces requests out).
- **Atomic and distributed:** one `EVALSHA` per decision; time comes from Redis, so there is no clock skew between instances. Proven by concurrency tests (exactly N of N+M allowed).
- **Two ways to use it:** a reverse-proxy gateway (`RL_UPSTREAM_URL`) or a decision API (`POST /v1/check`).
- **Multi-tenant:** per-tenant rule, API keys (stored hashed), disable switch; changes apply on all instances within milliseconds (Redis pub/sub).
- **Resilient:** circuit breaker (no 200 ms penalty per request during a Redis outage), per-tenant fail-open/closed policy, stale-if-error config cache, request pipelining, graceful drain on shutdown.
- **Observable:** Prometheus metrics with bounded label cardinality, a provisioned Grafana dashboard, structured logs.

## Architecture
```
 Client ──► [ gateway / decision API (Go, stateless) ] ──► Upstream service
                 │  API key → tenant → rule (cached, hot-reloaded)
                 │  pipelined EVALSHA, atomic per decision
                 ▼
              Redis ── Lua: token_bucket | sliding_window | leaky_bucket
                 ▲                                  ▲
        tenant config + pub/sub              redis_exporter
 Prometheus ◄── /metrics (private port)  ◄──┘        Grafana ◄── Prometheus
```
More detail: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start (Docker Compose)
```bash
docker compose up -d --build           # redis, demo upstream, app, prometheus, grafana
H="Authorization: Bearer dev-admin-token"   # demo admin token from docker-compose.yml

# 1. create a tenant: 5 requests per 10 s, sliding window
curl -X PUT -H "$H" localhost:8080/admin/v1/tenants/acme \
  -d '{"name":"Acme","rule":{"algorithm":"sliding_window","limit":5,"window":"10s"}}'

# 2. mint an API key (shown once)
KEY=$(curl -s -X POST -H "$H" localhost:8080/admin/v1/tenants/acme/keys | sed 's/.*"key":"\([^"]*\)".*/\1/')

# 3. send traffic through the gateway: 5 x 200, then 429 with Retry-After
for i in $(seq 7); do curl -s -o /dev/null -w "%{http_code} " -H "X-API-Key: $KEY" localhost:8080/hello; done

# 4. or ask for a decision without proxying
curl -X POST -H "X-API-Key: $KEY" localhost:8080/v1/check -d '{"key":"enduser42"}'
```
Grafana: http://localhost:3000 (dashboard "Rate Limiter Overview") · Prometheus: http://localhost:9090
The demo token and Grafana's anonymous access are for local use only; set `RL_ADMIN_TOKEN` to a strong secret in real deployments.

## Run without Docker
```bash
docker compose up -d redis
RL_ADMIN_TOKEN=secret RL_UPSTREAM_URL=http://localhost:9000 go run ./cmd/server
```
Go 1.24+. `make test` runs the suite (Redis-backed tests skip themselves if Redis is unreachable; CI runs them with `-race`).

## Documentation map
| Doc | Purpose |
|---|---|
| [docs/API.md](docs/API.md) | HTTP API, auth, headers, admin API, every configuration variable |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, request flow, failure behavior, data model |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Decision log (ADR-001 … ) with trade-offs |
| [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) | Metrics, dashboard, logging, cardinality rules |
| [docs/BENCHMARKS.md](docs/BENCHMARKS.md) | Load-test method, results, profile, what we can and cannot claim |
| [docs/ROADMAP.md](docs/ROADMAP.md) | The phased build and status |
| [docs/phases/](docs/phases/) | One journal per phase: steps taken, ideas, mistakes, test results |

## Repository layout
```
cmd/server        the service            cmd/loadgen   open-loop load generator
internal/limiter  algorithms (Lua), router, circuit breaker, Redis pipelining
internal/tenant   tenant store, API keys, cache, pub/sub hot reload
internal/{api,middleware,admin,server,auth,decision,rules,metrics,config,store}
deploy/           Prometheus + Grafana provisioning (dashboard is generated: deploy/gen_dashboard.py)
loadtest/         bench.sh (reproducible benchmark), summarize.py
.github/workflows CI: gofmt, vet, race tests against Redis, docker build, dashboard drift check
```

## Known limitations
- Redis is the single point of state: one Redis thread bounds throughput (~100k simple scripts/s in isolation); scale-out means sharding tenants across Redis instances (keys already carry a per-tenant hash tag). Redis Cluster is not tested.
- Rate-limit state is not persisted; a Redis restart resets counters (deliberate: faster, and limits are short-lived).
- Limits are per API key within a tenant; a tenant-wide shared quota is not implemented.
- The admin API uses one static bearer token (ADR-010).
- The 20k req/s @ p99 < 5 ms goal is unproven (see benchmarks).
