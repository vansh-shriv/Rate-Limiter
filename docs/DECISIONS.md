# Decision Log

## ADR-001: Go over Node
**Context:** goal is 20k req/s, p99 < 5 ms. **Decision:** Go. Goroutines handle concurrency cheaply, no event-loop stalls,
low GC pause, single static binary for Docker. **Trade-off:** more boilerplate than Node.

## ADR-002: Redis Lua scripts for atomicity
**Decision:** all algorithm logic in Lua executed via `EVALSHA`. Redis is single-threaded, so a script is atomic.
Alternatives rejected: `WATCH/MULTI` (retries under contention), distributed locks (extra round trips).

## ADR-003: Time source = Redis `TIME`
Avoids clock skew between gateway instances. (Redis 5+ allows `TIME` before writes in scripts via effects replication, default in 7.)

## ADR-004: Monorepo layout, `cmd/` + `internal/`
Standard Go layout; `internal/` prevents external import of unstable packages.

## ADR-005: Docs-as-journal
Each phase gets `docs/phases/phase-NN-*.md` recording steps, ideas, trade-offs, and the commit message.

## ADR-006: Tenant config in Redis, hot-reloaded via pub/sub
**Decision:** tenants live in Redis; each instance keeps a TTL cache and subscribes to `rlcfg:events` to invalidate on change. The TTL (30 s) is only the safety net for lost messages (e.g. during a reconnect).
**Why:** no extra database to run; all instances share one source of truth; changes are live in milliseconds. **Rejected:** polling only (slow, wasteful); config files (no multi-instance consistency).

## ADR-007: API keys are stored hashed; buckets are keyed by public key id
The SHA-256 of a key is the only thing stored or used in a Redis key; the plaintext is shown once at creation. Keys are 256-bit random, so a fast unsalted hash is appropriate (nothing to dictionary-attack). A Redis dump therefore yields no usable credentials. The bucket subject is `key:<first 16 hex of hash>`.

## ADR-008: Tenant comes from credentials, not from the request
`/v1/check` no longer accepts `tenant` in the body, and gateway mode no longer trusts `X-Tenant-ID` by default (`RL_AUTH_MODE=apikey`). Otherwise any caller could burn another tenant's quota or pick the most generous tenant. Header mode remains for local development, with a startup warning.

## ADR-009: Cache design: read-through, singleflight, short negative caching, bounded
Hits take only an RLock. Concurrent misses collapse into one Redis read. "Not found" is cached for at most 5 s so random keys can't hammer Redis; backend errors never are. A 10k-entry cap (expiry sweep, then flush) stops an attacker exhausting memory with unknown tenant ids or keys.

## ADR-010: Admin plane secured by one static bearer token (for now)
Simple and adequate for a service run by one team; compared in constant time via fixed-length hashes; the admin API is off unless `RL_ADMIN_TOKEN` is set. Later options: scoped/rotating tokens, mTLS, or a separate private listener.

## ADR-011: Pipeline concurrent decisions to Redis (Batcher), on by default
Each decision is one Redis round trip; under load that overhead dominates. `limiter.Batcher` coalesces calls that are *already waiting* into one pipeline (never waits to fill a batch, so no added latency when idle). Each Lua script stays atomic. Measured: +60% closed-loop throughput, never worse than direct. Default 2 flushers (weak evidence; re-tune per hardware). `RL_BATCH_FLUSHERS=0` disables.

## ADR-012: Benchmarks are open-loop and self-verifying; unproven claims are not made
Latency is measured from intended send time (no coordinated omission). The harness asserts which variant it runs and refuses to start on busy ports. The 20k @ p99<5 ms target was not met on the available hardware and is documented as such rather than quoted.

## ADR-013: Circuit breaker in front of Redis
Without it a Redis outage costs every request the full Redis timeout (200 ms) and accumulates goroutines. After N consecutive backend failures (default 5) the circuit opens and decisions fail in microseconds; after a cooldown one probe is allowed (half-open). Only backend failures count; validation errors and caller cancellation are ignored. Per process, no shared state. Exposed as `rl_circuit_open`.

## ADR-014: Failure policy is per tenant, read from last-known config
`fail_open` on a tenant overrides the global default, so tenants whose limits protect something fragile can fail closed while others fail open. It is read with a non-loading `peek` of the cache (ignoring expiry) because it is needed exactly when Redis cannot be asked. Unidentifiable requests (cold cache during an outage) use the global default.

## ADR-015: Stale-if-error for tenant config and API keys
An expired cache entry is served when the reload fails, with the next reload delayed 1 s. Keeps authentication and per-tenant policy working through a Redis outage and probes a dead Redis about once a second per key rather than once per request. Trade-off: a revoked key could keep working during an outage until Redis returns.

## ADR-016: Graceful shutdown: drain, finish, then close dependencies
SIGTERM flips `/readyz` to 503 (liveness stays green), waits `RL_SHUTDOWN_DELAY`, stops accepting and finishes in-flight requests, stops the metrics listener, then closes the pipeliner and Redis. Closing dependencies first would fail requests that were legitimately in flight.
