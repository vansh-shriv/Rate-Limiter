# Phase 7 — Observability

## Goal
See what the limiter is doing and be able to prove performance claims with data: Prometheus metrics, a Grafana dashboard, sane logging. Reference: [../OBSERVABILITY.md](../OBSERVABILITY.md).

## What was added
```
internal/metrics/              private Prometheus registry; implements decision.Observer + server.HTTPObserver
internal/server/instrument.go  per-route metrics + access-log wrapper (status recorder)
deploy/prometheus/             scrape config
deploy/grafana/                datasource + dashboard provisioning, generated dashboard JSON
deploy/gen_dashboard.py        dashboard generator
docker-compose.yml             + redis-exporter, prometheus, grafana
```
Config: `RL_METRICS_ADDR` (default `:9090`), `RL_METRICS_PER_TENANT` (default true), `RL_LOG_LEVEL` (default `info`). The build version is injected with `-ldflags -X main.version` (Dockerfile `ARG VERSION`).

## Design decisions
1. **Instrumentation by interface, not import.** `decision.Checker` has an optional `Observer`; `server` has an optional `HTTPObserver`; the tenant `Provider` has `ObserveCache`. Core packages never import Prometheus, and tests use fakes or nil.
2. **One measurement point for decision latency:** `Checker.Check` (rule lookup + Redis script). That is the number the "p99 < 5 ms" claim is about, and it excludes HTTP/proxy noise. HTTP timing is recorded separately per route.
3. **Separate metrics listener**, so internal data (tenant names, traffic) is never on the public port. In compose it isn't published to the host at all; only Prometheus can reach it.
4. **Cardinality discipline** (see OBSERVABILITY.md): tenant label only for resolved tenants (`unknown` otherwise), fixed route set, nothing client-controlled. This closes the classic hole where an attacker sprays random values until Prometheus runs out of memory. Verified: 60 requests with bogus API keys produced no new label values.
5. **Histogram buckets** are dense between 100 µs and 5 ms because the SLO lives there; default buckets would hide it.
6. **Private registry** (not the global default), so only intended collectors exist; Go and process collectors are added explicitly.
7. **`statusRecorder` implements `Unwrap`**, so `http.ResponseController` (used by `httputil.ReverseProxy` for flushing and streaming) still works. Covered by a test.
8. **Logs:** the per-request line is Debug only; 5xx is Warn; keys are never logged.
9. **Dashboard as code:** generated from a script and provisioned read-only. Each panel sits next to its PromQL, so a metric rename is a one-place change.

## Tests
- `metrics`: scrape output contains the expected series, labels and buckets; the per-tenant label collapses to `all` when disabled.
- `decision`: the observer gets the right result class for allowed / denied / backend error / cost-too-high / unknown tenant / rules backend error, and the tenant label is `unknown` whenever the rule did not resolve.
- `tenant`: the cache observer reports hit / miss / not_found / error.
- `server`: status capture (explicit, implicit 200, nothing written), in-flight gauge balanced, Flusher preserved.
- **End-to-end (Docker, real traffic: 2 tenants plus bogus keys):**
  - Prometheus targets `ratelimiter` and `redis` are both **up**.
  - `rl_decisions_total` splits by tenant and result (acme 56 allowed / 4 denied, globex 20 / 40).
  - `rl_http_requests_total` shows gateway 200 / 401 / 429.
  - The cache shows 118 hits against 2 misses, plus 60 `not_found` for the bogus keys.
  - Decision p99 is about 2 ms on this (slow, Docker Desktop) setup.
  - `/metrics` is not served on the public port.
  - Grafana is healthy, the datasource reports "Successfully queried the Prometheus API", and the dashboard loaded with 16 panels.

## Notes / gotchas
- Pinned `prometheus/client_golang v1.20.5` (Go 1.24 compatible) and confirmed `go.mod` still says `go 1.24` after `go mod tidy` (see the Phase 6 gotcha).
- Grafana and Prometheus images are pinned to specific versions for reproducibility. Anonymous Grafana access and `admin/admin` are demo-only and flagged as such in compose.
- Long multi-heredoc shell commands failed to parse in this environment twice; files are now written with the editor tool instead.
- Not done (ideas): alert rules (Prometheus rule file for error rate and p99), per-tenant dashboards via template variables, exemplars/tracing, a Redis latency panel from `redis_commands_duration_seconds_total`.

## Git
```
git add .
git commit -m "feat: phase 7 - Prometheus metrics, Grafana dashboard, structured logging, compose observability stack"
git push
```
