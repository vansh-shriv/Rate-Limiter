# Roadmap

Status: ⬜ not started · 🟨 in progress · ✅ done (committed)

| # | Phase | Key deliverables | Status |
|---|-------|------------------|--------|
| 0 | Planning & scaffold | Repo structure, docs, ADRs, architecture | ✅ |
| 1 | Go project + config + Redis plumbing | go.mod, config loader, Redis client, docker-compose (Redis), health endpoint | ✅ |
| 2 | Token bucket (Lua) | Atomic Lua script, Go wrapper, unit + concurrency tests | 🟨 |
| 3 | Sliding window (Lua) | Sliding-window-log/counter script, tests | ⬜ |
| 4 | Leaky bucket (Lua) | Script, tests, shared `Limiter` interface | ⬜ |
| 5 | HTTP API + middleware | `/v1/check` endpoint, drop-in middleware, rate-limit headers (429, Retry-After) | ⬜ |
| 6 | Per-tenant config | Tenant store in Redis, admin API, hot reload, API keys | ⬜ |
| 7 | Observability | Prometheus metrics, Grafana dashboard, structured logs | ⬜ |
| 8 | Load testing & optimization | k6/vegeta scenarios, pipelining/pooling tuning, BENCHMARKS.md | ⬜ |
| 9 | Hardening & polish | Fail-open/closed policy, graceful shutdown, CI, final README | ⬜ |

Each phase has a journal in `docs/phases/phase-NN-*.md`.
