# Phase 3 — Sliding Window (Lua)

## Goal
Second algorithm: an **exact** sliding window limiter, atomic in one Redis script.

## Algorithm (sliding window *log*)
State per key: a sorted set. Score = admit time (ms, from Redis `TIME`), member = `"<random request id>:<i>"`.
In one script:
1. `ZREMRANGEBYSCORE key -inf (now - window)` — evict entries that slid out.
2. `count = ZCARD key`.
3. If `count + cost <= limit` → `ZADD` `cost` members at `now`, allowed.
4. Else denied. `retry_after` = when the `(count + cost - limit)`-th oldest entry expires = `score + window - now`.
5. `PEXPIRE key window` so idle keys vanish; `reset_after` = newest entry's expiry.

## Files
- `internal/limiter/scripts/sliding_window.lua`
- `internal/limiter/slidingwindow.go` — `SlidingWindow.Allow(ctx, key, SlidingWindowRule{Limit, Window}, cost)`
- `internal/limiter/slidingwindow_test.go`
- `internal/limiter/limiter.go` — **refactor:** `Result` and shared errors moved here from `tokenbucket.go`.

## Design notes / trade-offs
- **Log vs counter:** the log is *exact* — never admits more than `limit` in any trailing window — but costs O(limit) memory and O(log N) per op. The cheaper sliding-window *counter* (two fixed-window counts, weighted) is O(1) but approximate. Chosen: log, for correctness. A counter variant can be added later as an option for very high limits (see ROADMAP Phase 9 ideas).
- **Unique members:** `ZADD` with an existing member just updates its score, which would silently under-count. Each call carries a random 64-bit id from Go (`math/rand/v2`), plus `:i` per unit of cost. (Alternative rejected: a per-key sequence counter — needs a second key, which breaks Redis Cluster single-slot rule.)
- **Denied requests are not recorded** — a client hammering a full window can't extend its own penalty.
- **Cost > 1** is all-or-nothing; a denied call consumes nothing (tested).
- **Boundary burst test:** fill the limit, wait half a window, ask for the full limit again → denied, with `RetryAfter ≈ half a window`. A fixed-window counter would allow 2× here.

## Tests (all passing, plus phase 2's)
Limit then deny · slides over time · no boundary burst + retry-after accuracy · cost/validation · TTL · concurrency (2000 attempts → exactly 100 allowed, ZCARD = 100, so no member collisions).

## Benchmarks (same noisy Docker-Desktop Redis as phase 2 — see its caveats)
| Limiter | ns/op | allocs/op |
|---|---|---|
| Token bucket | ~118k | 13 |
| Sliding window | ~137k | 15 |
Numbers vary run to run (token bucket was 187k last phase); the environment dominates. Real numbers come in Phase 8.

## Git
```
git add .
git commit -m "feat: phase 3 - exact sliding window log limiter (Redis Lua) with tests"
git push
```
