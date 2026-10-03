# Distributed Rate Limiter as a Service

A drop-in API gateway / middleware offering **token bucket**, **sliding window**, and **leaky bucket**
rate limiting, backed by **Redis Lua scripts** (atomic, distributed), with **per-tenant configuration**
and a **Prometheus + Grafana metrics dashboard**.

**Target:** handle **20k req/s with p99 < 5 ms** (to be measured and proven in Phase 8 — see `docs/BENCHMARKS.md`).

## Stack
Go 1.24 · Redis 7 · Prometheus · Grafana · Docker Compose · k6 / vegeta (load testing)

## Quick start
```
docker compose up -d --build
curl localhost:8080/readyz
```

## Status
See [docs/ROADMAP.md](docs/ROADMAP.md) for phase-by-phase progress.

## Documentation map
| Doc | Purpose |
|---|---|
| [docs/ROADMAP.md](docs/ROADMAP.md) | Phases, deliverables, status, commit messages |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System design and data flow |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Decision log (why Go, why Lua, etc.) |
| [docs/phases/](docs/phases/) | One journal per phase: every step, idea, and trade-off |
| docs/BENCHMARKS.md | Load-test results (created in Phase 8) |

## Workflow
Built phase by phase. After each phase: review → commit → push → proceed. Commit messages are listed in each phase doc.
