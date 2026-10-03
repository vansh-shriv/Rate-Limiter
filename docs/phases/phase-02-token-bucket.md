# Phase 2 — Token Bucket (Lua)

## Goal
First algorithm: a distributed token bucket whose check-and-consume is one atomic Redis script.

## Algorithm
State per key (Redis hash): `tokens` (float), `ts` (ms of last update).
On each request, inside one script:
1. `now` = Redis `TIME` (ms).
2. **Lazy refill:** `tokens = min(capacity, tokens + elapsed_ms * rate / 1000)`. No background job.
3. If `tokens >= cost` → subtract, allowed; else denied with `retry_after = ceil((cost - tokens) / rate)`.
4. Write back, `PEXPIRE` = full-refill time + 1 s (an idle bucket equals a full one, so dropping it is lossless).
5. Return `{allowed, remaining, retry_after_ms, reset_after_ms}`.

New buckets start **full** (allows an initial burst up to capacity).

## Files
- `internal/limiter/scripts/token_bucket.lua` — the script (embedded with `go:embed`).
- `internal/limiter/tokenbucket.go` — `TokenBucket.Allow(ctx, key, rule, cost)`, `Result`, `TokenBucketRule`, errors.
- `internal/limiter/tokenbucket_test.go` — tests + benchmark (need Redis; auto-skip if unreachable, `RL_TEST_REDIS_ADDR` overrides).

## Design notes
- **Atomicity:** Redis runs a script to completion before any other command → no lost updates. Proven by `TestTokenBucketConcurrentAtomicity`: 50 goroutines × 40 attempts (2000) on one key with capacity 100 and ~zero refill → **exactly 100 allowed, 1900 denied**.
- **`redis.Script.Run`** = `EVALSHA`, falls back to `EVAL` on `NOSCRIPT` (e.g. after Redis restart / `SCRIPT FLUSH`). One round trip per decision.
- **Redis `TIME`, not client time:** no clock skew between gateway instances.
- **Float precision:** Lua doubles; stored via Redis' `%.14g` formatting — negligible drift for rate limiting.
- **Validation in Go, not Lua:** reject `cost <= 0`, `cost > capacity` (can never succeed), and non-positive capacity/rate before touching Redis.
- **Keys use `{hash-tag}`** style in tests (`rl:{tenant}:...` in prod) for future Redis Cluster compatibility.
- Interface generalization (`Limiter` shared by all algorithms) is deliberately deferred to Phase 4, once we have 3 real implementations to abstract over.

## Tests
Burst-then-deny · refill over time · cost > 1 and validation errors · TTL is set · concurrent atomicity.

## Benchmark & an important finding
`BenchmarkTokenBucketAllow` (256 parallel goroutines, 64 keys, Redis in Docker Desktop on Windows):
**~187 µs/op ≈ 5.3k decisions/s.**

Baseline check: `redis-benchmark` *inside the container* on plain `PING` only reached **~19k rps at ~1.3 ms avg latency**.
So the Docker Desktop VM, not our script, is the ceiling on this machine. Consequences for Phase 8:
- The 20k req/s / p99 < 5 ms claim must be measured on a Redis that isn't throttled by the Docker Desktop VM (e.g. Redis inside WSL2 natively, or a Linux host/VM), and the environment documented honestly.
- Optimizations to evaluate: client-side **pipelining/batching** of checks, fewer allocations (14 allocs/op now), pool tuning, `redis.Script` → raw `EvalSha` to cut overhead.
- Never quote a number without stating hardware + Redis placement.

## Git
```
git add .
git commit -m "feat: phase 2 - atomic Redis Lua token bucket with concurrency tests and benchmark"
git push
```
