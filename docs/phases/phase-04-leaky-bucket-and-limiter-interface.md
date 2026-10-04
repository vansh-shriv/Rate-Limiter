# Phase 4 — Leaky Bucket (Lua) & the Shared `Limiter` Interface

## Goal
Third algorithm, plus one algorithm-agnostic `Rule` and `Limiter` interface so the HTTP layer (Phase 5) and tenant config (Phase 6) don't care which algorithm runs.

## Leaky bucket: meter vs shaper
A leaky bucket used as a *meter* is mathematically the mirror of a token bucket (level goes up instead of tokens going down) — it would add nothing. We implement it as a **shaper (queue)**, which is what makes it distinct:
- Requests enter a virtual FIFO queue draining at a constant rate (one unit per `interval = 1000/rate` ms).
- **No bursts:** back-to-back requests are admitted but each gets a `Delay` (0, 100 ms, 200 ms, …) so downstream sees a constant rate.
- If the queue is full → rejected, with `RetryAfter` = time until room appears.

State is a single number, `next_free` (when the queue empties), stored with `SET … PX`. Script:
`wait = max(next_free, now) - now`; admit iff `wait <= (capacity - cost) * interval`; then `next_free = max(next_free, now) + cost * interval`.
(This is the "virtual scheduling" form of GCRA.) Rejections write nothing.

## Algorithm comparison
| | Token bucket | Sliding window (log) | Leaky bucket (shaper) |
|---|---|---|---|
| Allows bursts | yes, up to capacity | up to limit anywhere in window | no — spaced out |
| Output | bursty | bursty | constant rate |
| State | 2 fields | O(limit) sorted set | 1 number |
| Precision | exact | exact | exact |
| Best for | APIs wanting burst tolerance | strict quotas | protecting a fragile downstream |

## The shared abstraction (`router.go`)
```go
type Rule struct { Algorithm; Limit int64; Window time.Duration; Burst int64 }   // "Limit per Window"
type Limiter interface { Allow(ctx, key string, rule Rule, cost int64) (Result, error) }
```
- `Router` implements `Limiter` and dispatches to the three concrete limiters, mapping the unified rule: token bucket refill = `Limit/Window`, capacity = `Burst` (default `Limit`); leaky bucket drain = `Limit/Window`, queue = `Burst`; sliding window ignores `Burst`.
- The rule is passed **per call**, not per limiter instance → per-tenant rules in Phase 6 need no new limiter objects.
- `Rule.Validate()` runs before Redis is touched.
- `Result` gained `Delay` (leaky bucket only).
- Concrete limiters keep their own typed rules/APIs; the interface only lives at the edge.

## Tests (22 pass, concurrency tests included)
Leaky: spacing of delays · drains over time · cost + validation + TTL · concurrency (2000 attempts → exactly 100 admitted).
Router: `Rule.Validate` table · all three algorithms through the interface enforce limit 5 · burst override · invalid rule rejected.

## Notes / gotchas
- **`-race` can't run on this machine** (no working 64-bit cgo gcc → `cc1.exe: 64-bit mode not compiled in`). `Makefile` now has `make test` (plain) and `make test-race` (for Linux/CI in Phase 9). Concurrency tests still verify atomicity end-to-end against Redis; the race detector would only cover Go-side data races.
- `Rule.Window` is a `time.Duration`, which JSON-encodes as integer nanoseconds. Phase 6 (tenant config) will add a human-friendly `"1s"` parser.
- `next_free` is stored with `%.3f` — ms-precision on ~1.7e12 values is within double precision.

## Git
```
git add .
git commit -m "feat: phase 4 - leaky bucket shaper (Redis Lua) and unified Limiter interface/Router"
git push
```
