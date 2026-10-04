# Observability

## Endpoints and UIs (docker compose)
| What | Where | Notes |
|---|---|---|
| Service metrics | `app:9090/metrics` | **Separate listener** (`RL_METRICS_ADDR`, empty disables). Not published to the host and never on the public/gateway port. |
| Prometheus | http://localhost:9090 | scrapes `app:9090` and `redis-exporter:9121` every 5 s |
| Grafana | http://localhost:3000 | dashboard **Rate Limiter Overview** (folder *Rate Limiter*), anonymous viewer; admin login `admin`/`admin` (demo only) |

## Metric reference
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `rl_decisions_total` | counter | `tenant`, `algorithm`, `result` | one per decision. `result`: `allowed`, `denied`, `rejected` (unknown/disabled tenant, invalid cost/rule), `error` (backend failure) |
| `rl_decision_duration_seconds` | histogram | `algorithm`, `result` | rule lookup + Redis script; buckets dense from 100 µs to 5 ms |
| `rl_http_requests_total` | counter | `route`, `code` | `route` in {`check`, `gateway`, `admin`} |
| `rl_http_request_duration_seconds` | histogram | `route` | gateway includes upstream time |
| `rl_http_in_flight_requests` | gauge | – | |
| `rl_config_cache_total` | counter | `cache` (`tenant`/`apikey`), `result` | `hit`, `miss`, `not_found`, `error`, `stale` (served an expired entry because Redis failed), `coalesced` |
| `rl_circuit_open` | gauge | – | 1 while the Redis circuit breaker is open (decisions fail fast); **alert on this** |
| `rl_build_info` | gauge | `version` | always 1 |
| `go_*`, `process_*` | – | – | runtime |

Redis metrics come from `redis_exporter` (`redis_commands_processed_total`, `redis_memory_used_bytes`, ...).

## Cardinality rules (important)
Nothing derived from client input is ever a label: no API keys, IPs, paths or raw tenant ids from requests.
- `tenant` is set **only after the tenant's rule resolved**, i.e. it exists in config; otherwise `unknown`. Bounded by configured tenants. Set `RL_METRICS_PER_TENANT=false` to collapse it to `all` for very large tenant counts.
- `route` is picked from a fixed set in code, not from the URL path.

## Useful queries
```
sum(rate(rl_decisions_total[1m]))                                          # decisions/s
sum(rate(rl_decisions_total{result="denied"}[1m])) / sum(rate(rl_decisions_total{result=~"allowed|denied"}[1m]))
histogram_quantile(0.99, sum by (le) (rate(rl_decision_duration_seconds_bucket[1m])))   # p99 (target < 5 ms)
sum by (tenant) (rate(rl_decisions_total{result="denied"}[1m]))            # who is being throttled
```
The `error` rate is the one to alert on (Redis trouble). A rising `rejected` rate usually means a misconfigured client or key probing.

## Logging
JSON via `slog`. `RL_LOG_LEVEL` = `debug|info|warn|error` (default `info`).
- Per-request access logs are **Debug** (a line per request is too expensive at 20k req/s); 5xx responses log at **Warn**.
- Backend errors are logged with the tenant and the fail-open/closed policy. Raw API keys are never logged.

## Dashboard
`deploy/grafana/dashboards/ratelimiter.json` is **generated** by `python deploy/gen_dashboard.py` (edit the script, regenerate, commit both). It is provisioned read-only from files, so it is identical on every environment.

Panels: decisions/s, denied ratio, decision p99 (red above 5 ms), backend errors, cache hit ratio; decisions by result / algorithm / tenant; latency p50/p95/p99 with a 5 ms threshold line; HTTP rate and p99 per route; config cache outcomes; in-flight requests; Redis commands, memory and clients; Go goroutines and heap.
