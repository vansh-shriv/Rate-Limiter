# Architecture

## Overview
```
 Client ──► [ gateway / decision API (Go, stateless, N instances) ] ──► Upstream service
                 │
                 │  1. identify: API key ─► tenant        (in-process cache, pub/sub invalidated)
                 │  2. rule:     tenant  ─► algorithm, limit, window, burst, fail policy
                 │  3. decide:   one atomic Lua script per request (pipelined with its neighbours)
                 ▼
              Redis ◄── Lua: token_bucket / sliding_window / leaky_bucket
                 ▲
 Prometheus ◄── /metrics (private listener)       Grafana ◄── Prometheus
```

## Request flow (gateway mode)
1. `server` routes: `/healthz`, `/readyz`, `/admin/*`, `/v1/check` are explicit routes; everything else goes to the gateway.
2. `middleware` asks the `auth.Identifier` (API key → tenant) who is calling. Bad/missing key → 401, unknown/disabled tenant → 403.
3. `decision.Checker` resolves the tenant's `limiter.Rule` (`rules.Provider`, cached) and builds the Redis key `rl:{tenant}:{algo}:{subject}`.
4. `limiter.Breaker` (fail fast when Redis is unhealthy) → `limiter.Router` (picks the algorithm) → `limiter.Batcher` (pipelines concurrent EVALSHAs) → Redis.
5. Result → headers (`X-RateLimit-*`, `Retry-After`) → 429, or (leaky bucket) hold for `Delay`, then reverse-proxy to the upstream. `X-API-Key` is stripped before proxying.

## Components
| Package | Responsibility |
|---|---|
| `internal/limiter` | the three algorithms (embedded Lua), `Rule`/`Limiter`/`Router`, `Breaker`, `Batcher`, key scheme |
| `internal/tenant` | tenant model + validation, Redis `Store`, `ttlCache`, `Provider` (rules + identity + fail policy), pub/sub `Watch` |
| `internal/decision` | one decision path shared by API and middleware; observer + fail-policy hooks; rate-limit headers |
| `internal/middleware`, `internal/api` | gateway middleware, `POST /v1/check` |
| `internal/admin` | tenant and API-key management behind a bearer token |
| `internal/server` | routing, access log + per-route metrics, hardened reverse proxy, drain flag |
| `internal/auth` | credential extraction, hashing, `Identifier` implementations |
| `internal/metrics` | private Prometheus registry; implements the observer interfaces |
| `internal/config`, `internal/store` | environment configuration; pooled Redis client |

## Design principles
1. **Atomicity:** every decision is one Lua script, so there is no read-modify-write race between gateway instances. (Tests: exactly N of N+M concurrent requests admitted, for every algorithm, also through the pipeliner.)
2. **Time from Redis `TIME`:** no clock skew between instances.
3. **Stateless gateway:** all state lives in Redis; scale by adding instances.
4. **Bounded memory:** every key has a TTL; sliding-window `limit` is capped; caches and metric labels are bounded (attacker input can't grow them).
5. **Secure by default:** tenant comes from a hashed API key, never from a client-supplied header; admin API is off unless a token is set; `X-Forwarded-For` is not trusted; metrics/pprof are on a private listener.
6. **Interfaces at the seams:** `limiter.Limiter`, `rules.Provider`, `auth.Identifier`, `decision.Observer`, `decision.Policy`, `server.HTTPObserver`. Core packages never import Prometheus or Redis-specific wiring, so most logic is tested with fakes.

## Redis data model
| Key | Type | Purpose |
|---|---|---|
| `rl:{tenant}:token_bucket:<subject>` | hash | tokens, last-update ms |
| `rl:{tenant}:sliding_window:<subject>` | sorted set | admit-time log |
| `rl:{tenant}:leaky_bucket:<subject>` | string | time the virtual queue empties |
| `rlcfg:{id}:doc` / `:keys` | string / hash | tenant JSON / key id → key hash |
| `rlcfg:apikey:<sha256>` | string | key hash → tenant id |
| `rlcfg:tenants` | set | tenant ids |
| `rlcfg:events` | pub/sub | cache invalidation (`<tenant id>` or `*`) |

## Failure behavior (what happens when things break)
| Situation | Behavior |
|---|---|
| Redis down | First `RL_BREAKER_FAILURES` (5) requests each wait out `RL_REDIS_TIMEOUT` (200 ms); then the **circuit opens** and decisions fail in microseconds. Each request then follows its tenant's policy: `fail_open: true` → proxied, `false` → 503; unset → global `RL_FAIL_OPEN`. After `RL_BREAKER_COOLDOWN` one probe is allowed; success closes the circuit. `/readyz` reports 503, `/healthz` stays 200. |
| Redis down, config cache warm | Tenant rules and API keys keep resolving from the cache (**stale-if-error**, one reload attempt per key per second), so authentication and per-tenant policy keep working during the outage. |
| Redis down, cache cold | Identity can't be resolved: the global policy applies. |
| Pub/sub message lost | Config changes apply when the cache entry expires (`RL_TENANT_CACHE_TTL`, 30 s). |
| Upstream down / slow | 502 / 504 JSON (`RL_UPSTREAM_TIMEOUT`), logged; client disconnects are not logged as errors. |
| SIGTERM | `/readyz` → 503 immediately; wait `RL_SHUTDOWN_DELAY` so load balancers drain; stop accepting, finish in-flight requests (≤ `RL_SHUTDOWN_TIMEOUT`); stop the metrics listener; close the pipeliner, then Redis. |
| Wrong API key / unknown tenant | 401 / 403; never fail-open. |

## Scaling notes
- One Redis thread is the throughput ceiling (script ≈ 10 µs isolated). Pipelining raises the per-connection ceiling; beyond that, shard tenants over several Redis instances (a tenant's keys share a hash tag, so a tenant maps to one shard).
- Gateway instances scale horizontally; they share nothing but Redis.
