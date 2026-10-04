# Distributed Rate Limiter as a Service

A drop-in API gateway / middleware offering **token bucket**, **sliding window**, and **leaky bucket**
rate limiting, backed by **Redis Lua scripts** (atomic, distributed), with **per-tenant configuration**
and a **Prometheus + Grafana metrics dashboard**.

**Target:** 20k req/s with p99 < 5 ms. **Measured so far:** ~10k req/s at p99 ≈ 3-4 ms (5k at ≈ 2 ms) full-stack on a shared 8-core laptop under WSL2; the 20k target was **not** met on that hardware. Methodology, caveats and how to re-measure: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

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
| [docs/API.md](docs/API.md) | HTTP API, headers, configuration reference |
| [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) | Metrics, dashboard, logging, cardinality rules |
| [docs/phases/](docs/phases/) | One journal per phase: every step, idea, and trade-off |
| [docs/BENCHMARKS.md](docs/BENCHMARKS.md) | Load-test method, results, profile, what we can and cannot claim |

## Workflow
Built phase by phase. After each phase: review → commit → push → proceed. Commit messages are listed in each phase doc.
