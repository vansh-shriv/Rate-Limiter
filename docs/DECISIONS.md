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
