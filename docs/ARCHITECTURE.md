# Architecture

## Overview
```
 Client ──► [Gateway / Middleware (Go)] ──► Upstream service
                  │
                  │ EVALSHA (1 round trip, atomic)
                  ▼
               Redis  ◄── Lua scripts: token_bucket / sliding_window / leaky_bucket
                  ▲
 Prometheus ◄── /metrics        Grafana ◄── Prometheus
```

## Components
- **Limiter core (`internal/limiter`)** — `Limiter` interface; three Redis/Lua-backed implementations.
- **Tenant config (`internal/tenant`)** — per-tenant algorithm, limit, window/refill, burst; stored in Redis, cached in-process.
- **HTTP layer (`internal/api`, `internal/middleware`)** — `/v1/check` decision API + reverse-proxy/middleware mode.
- **Metrics (`internal/metrics`)** — Prometheus counters/histograms (allowed, denied, latency, Redis errors).
- **Deploy (`deploy/`)** — docker-compose: app, Redis, Prometheus, Grafana.

## Key design principles
1. **Atomicity:** every decision is a single Lua script → no read-modify-write races across gateway instances.
2. **One round trip:** `EVALSHA` per check; script time comes from Redis `TIME` (no client clock skew).
3. **Stateless gateway:** all state in Redis → horizontal scaling.
4. **Bounded memory:** every key gets a TTL.
5. **Explicit failure policy:** fail-open or fail-closed per tenant when Redis is unavailable.

## Redis key scheme (draft)
`rl:{tenant}:{algo}:{subject}` — hash-tag `{tenant}` reserved to allow Redis Cluster slotting later.
