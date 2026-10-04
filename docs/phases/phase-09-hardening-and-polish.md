# Phase 9 — Hardening & Polish

## Goal
Make the failure modes of a rate limiter boring: when Redis dies, when an upstream dies, when the process is told to stop. Then finish the project (CI, docs).

## The core problem
A limiter sits on the hot path of everything. Its failure must not become everyone's failure, and it must not silently turn "limits" into "no limits" for the tenants who need them.

## What was added
```
internal/limiter/breaker.go     circuit breaker around any Limiter
internal/tenant/cache.go        stale-if-error + peek; Provider.FailOpen (per-tenant policy from last known config)
internal/decision               Policy interface + Checker.ShouldFailOpen
internal/server/server.go       drain flag on /readyz, http timeouts, hardened reverse proxy
cmd/server/main.go              breaker wiring, graceful-shutdown ordering
.github/workflows/ci.yml        gofmt, vet, race tests vs Redis, docker build, dashboard drift check
deploy/gen_dashboard.py         + circuit-breaker, circuit-over-time and stale-config panels
README.md, docs/ARCHITECTURE.md rewritten for the finished system
```
New config: `RL_BREAKER_FAILURES` (5), `RL_BREAKER_COOLDOWN` (2s), `RL_SHUTDOWN_DELAY` (0s), `RL_UPSTREAM_TIMEOUT` (30s); tenants gained `fail_open`.

## Decisions (see ADR-013 … ADR-016)
1. **Circuit breaker.** Without it, a dead Redis costs every request the full 200 ms Redis timeout and piles up goroutines. After 5 consecutive backend failures the circuit opens and requests fail in microseconds; after the cooldown exactly one probe goes through. Only *backend* failures count; bad input and the client cancelling its own request say nothing about Redis health.
2. **Per-tenant fail policy.** `fail_open` on the tenant overrides the global `RL_FAIL_OPEN`. A free tier can fail open; a tenant whose limit protects a fragile or paid resource can fail closed (503). The decision uses the *last known* config via a non-loading `peek`, because the policy is needed exactly when Redis can't be asked.
3. **Stale-if-error cache.** An expired tenant/key entry is served if the reload fails, and the next reload is delayed 1 s, so a dead Redis is probed about once a second per key instead of once per request. Effect: authentication and per-tenant policy keep working through an outage. If nothing is cached, the error surfaces and the global policy applies.
4. **Graceful shutdown order:** `/readyz` → 503 immediately (liveness stays green, or an orchestrator would kill the process mid-drain); wait `RL_SHUTDOWN_DELAY`; stop accepting and finish in-flight requests; stop the metrics listener (kept up so the last scrape sees the drain); only then close the pipeliner and Redis.
5. **Proxy hardening.** Explicit transport (the default keeps only **2 idle connections per host**, which throttles a gateway under load); upstream header timeout → 504; connection errors → 502 JSON; `X-API-Key` stripped so the limiter's credential never reaches the upstream; client disconnects aren't logged as errors. No `WriteTimeout` (streaming responses); `ReadTimeout`/`IdleTimeout` set.
6. **CI** runs the race detector against a real Redis. That could not run on the dev machine (no 64-bit C toolchain for cgo), so CI is where data races would first show up. **First CI run: all green**, so the race detector found no data races in the whole suite (including the concurrency, batcher, breaker and pub/sub tests). Timings: tests with `-race` against Redis ≈ 1m29s, docker build ≈ 36s, dashboard drift check ≈ 5s.

## Tests
- `breaker`: opens after N consecutive failures and then never calls the backend; non-consecutive failures don't trip it; half-open probe closes on success; failed probe re-opens; **only one probe at a time**; caller cancellation and validation errors are ignored.
- `cache`: stale served during outage with reload backoff, recovery replaces the stale value, nothing cached → error surfaces, `peek` ignores expiry but not negatives.
- `decision`/`middleware`: tenant policy beats the global default in both directions.
- `tenant` (Redis): policy read from last known config without touching Redis.
- `server`: draining readiness (healthz stays 200), proxy strips the API key but keeps `Authorization`, adds `X-Forwarded-For`, 502 when upstream is down, 504 on a slow upstream within the timeout.
- `config`/`metrics`: new settings validated; breaker gauge.
- **End-to-end outage drill (Docker):**
  - Healthy baseline: 200s in 11–34 ms.
  - `docker compose stop redis`: the first 5 requests took ~208 ms each (Redis timeout), then the breaker opened and requests dropped to 6–22 ms.
  - Per-tenant policy during the outage: `strict` (fail_open=false) → **503**, `lenient` (true) → **200**, `plain` (global default open) → **200**.
  - `/readyz` → 503; `rl_circuit_open` = 1 (read from the app's private metrics port).
  - `docker compose start redis`: recovered in ~0.5 s, all three tenants 200, gauge back to 0.
  - **SIGTERM with a 3 s drain delay:** `/readyz` 503 and `/healthz` 200 during the drain; exit code 0.
- Grafana loads the 19-panel dashboard; Prometheus scrapes the new gauge.

## A flaky test found and fixed
Two pub/sub tests failed once when the whole suite ran in parallel and passed 3/3 alone. Cause: the helper slept 100 ms and *hoped* the subscription existed; a message published earlier is never seen. Fixed by polling Redis (`PUBSUB NUMSUB`) until the new subscriber is registered. Full suite then passed 3 runs in a row. The same property is a (documented) production trade-off: config changes made while an instance is (re)subscribing are picked up by the cache TTL.

## Known limitations
- Failure policy for requests that can't be identified (cold cache + Redis down) is the global default.
- The circuit breaker is per process; each instance learns about the outage separately (a few slow requests each).
- No admin-endpoint brute-force protection beyond the strength of the token.
- Redis Cluster, tenant-wide shared quotas, and the 20k req/s @ p99 < 5 ms claim remain open (see README and BENCHMARKS).

## Git
```
git add .
git commit -m "feat: phase 9 - circuit breaker, per-tenant fail policy, stale-if-error cache, graceful drain, hardened proxy, CI"
git push
```
