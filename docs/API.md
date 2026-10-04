# API Reference

## Operational
| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness (Redis ping); 503 if Redis is down **or the process is draining after SIGTERM** |

Never rate limited, never authenticated.

## Authentication (data plane)
Default mode `RL_AUTH_MODE=apikey`: every request to `/v1/check` and the gateway must carry a tenant API key —
`X-API-Key: <key>` or `Authorization: Bearer <key>`. The key determines the **tenant** (and therefore the rule);
the **bucket** (subject) is the key's public id, so limits are per API key.
Missing/invalid key → `401` + `WWW-Authenticate`; unknown or disabled tenant → `403`. Auth failures are never fail-open.

`RL_AUTH_MODE=header` is development-only: the tenant is taken from `X-Tenant-ID` (unauthenticated), the bucket from the API key if sent, else the client IP.

## Decision API — `POST /v1/check`
Ask "may this request proceed?" without proxying anything. The tenant comes from the API key, never from the body.
```
POST /v1/check        X-API-Key: rlk_...
{"key": "enduser42", "cost": 1}      # key = who/what is limited within your tenant; cost defaults to 1
```
| Status | Meaning |
|---|---|
| 200 | allowed |
| 429 | denied (body + `Retry-After` tell when to retry) |
| 400 | bad JSON, missing `key`, unknown field (including `tenant`), `cost` > capacity |
| 401 / 403 | missing-or-invalid key / unknown-or-disabled tenant |
| 503 | backend (Redis) error and fail-closed |

200/429 body:
```json
{"allowed":true,"algorithm":"token_bucket","limit":5,"remaining":4,"retry_after_ms":0,"reset_after_ms":2000,"delay_ms":0}
```
`delay_ms` (leaky bucket only) = how long the caller should hold the request.
With fail-open and a backend error: `200`, `allowed:true`, header `X-RateLimit-Status: degraded`.

## Gateway mode — everything else
Set `RL_UPSTREAM_URL`; all other paths are authenticated, rate limited, then reverse-proxied.
- **Headers:** `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` (seconds), and on 429 `Retry-After` (seconds, at least 1).
- Leaky bucket: admitted requests are held for `Delay` (cancelled if the client disconnects).
- `X-Forwarded-For` is **not** trusted (client-controlled) when choosing the bucket; the proxy still appends the client IP for the upstream.
- `X-API-Key` is stripped before the request reaches the upstream; `Authorization` is passed through untouched.
- Upstream failures: `502` (connection refused/reset) or `504` (no response headers within `RL_UPSTREAM_TIMEOUT`), JSON body.
- Redis down: the request follows the tenant's `fail_open` policy (proxied, or `503`).

## Admin API — `/admin/v1` (control plane)
Enabled only when `RL_ADMIN_TOKEN` is set. Every call needs `Authorization: Bearer <admin token>` (constant-time compared), else `401`.

| Method & path | Purpose |
|---|---|
| `PUT /admin/v1/tenants/{id}` | create/replace a tenant (idempotent) |
| `GET /admin/v1/tenants` · `GET .../{id}` | list / fetch |
| `DELETE /admin/v1/tenants/{id}` | delete tenant **and revoke all its keys** (204) |
| `POST /admin/v1/tenants/{id}/keys` | mint an API key — **plaintext returned once** (201) |
| `GET /admin/v1/tenants/{id}/keys` | list key ids (never plaintext) |
| `DELETE /admin/v1/tenants/{id}/keys/{keyID}` | revoke (204) |

Tenant body:
```json
{"name": "Acme", "disabled": false, "fail_open": false,
 "rule": {"algorithm": "sliding_window", "limit": 100, "window": "1m", "burst": 0}}
```
- `fail_open` (optional): what to do when Redis is unavailable. `true` = let this tenant's traffic through, `false` = reject with 503 (use for tenants whose limits protect something fragile or paid). Omitted = use the global `RL_FAIL_OPEN`. Takes effect from the last config the instance saw, so it still applies during a Redis outage.
- `id`: `[a-zA-Z0-9_-]{1,64}`. `algorithm`: `token_bucket` | `sliding_window` | `leaky_bucket`. `window`: Go duration (`30s`, `1m`, `24h`).
- `burst` (token/leaky bucket) defaults to `limit`. `sliding_window` `limit` must be at most 100 000 (memory is O(limit)).
- Changes reach **all instances within milliseconds** (pub/sub); `RL_TENANT_CACHE_TTL` bounds staleness if a message is ever lost.
- Changing a tenant's *algorithm* starts fresh state (Redis keys include the algorithm name); changing only limit/window keeps counters.

```
H="Authorization: Bearer $RL_ADMIN_TOKEN"
curl -X PUT  -H "$H" localhost:8080/admin/v1/tenants/acme -d '{"name":"Acme","rule":{"algorithm":"token_bucket","limit":100,"window":"1m"}}'
curl -X POST -H "$H" localhost:8080/admin/v1/tenants/acme/keys        # -> {"key":"rlk_...","key_id":"..."}
curl -H "X-API-Key: rlk_..." localhost:8080/anything                  # proxied, limited by acme's rule
```

## Configuration (env)
| Var | Default | |
|---|---|---|
| `RL_HTTP_ADDR` | `:8080` | |
| `RL_REDIS_ADDR` / `_PASSWORD` / `_DB` / `_POOL_SIZE` / `_TIMEOUT` | `localhost:6379` / – / 0 / 100 / 200ms | |
| `RL_UPSTREAM_URL` | – | enables gateway mode |
| `RL_ADMIN_TOKEN` | – | enables the admin API |
| `RL_AUTH_MODE` | `apikey` | `apikey` or `header` (dev only) |
| `RL_TENANT_CACHE_TTL` | `30s` | staleness bound for cached tenant config |
| `RL_FAIL_OPEN` | `true` | global default when Redis errors; a tenant's `fail_open` overrides it |
| `RL_BREAKER_FAILURES` / `_COOLDOWN` | `5` / `2s` | consecutive Redis failures that open the circuit; open time before a probe. `0` failures disables the breaker |
| `RL_BATCH_FLUSHERS` / `RL_BATCH_MAX` | `2` / `128` | Redis pipelining; `0` flushers = one round trip per decision |
| `RL_UPSTREAM_TIMEOUT` | `30s` | gateway: max wait for upstream response headers (else 504) |
| `RL_SHUTDOWN_DELAY` / `RL_SHUTDOWN_TIMEOUT` | `0s` / `10s` | after SIGTERM: stay not-ready this long before closing listeners / max time to finish in-flight requests |
| `RL_METRICS_ADDR` | `:9090` | private metrics listener (`""` disables) |
| `RL_METRICS_PER_TENANT` | `true` | tenant label on decision metrics |
| `RL_PPROF_ENABLED` | `false` | serve `/debug/pprof` on the metrics listener |
| `RL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `RL_DEFAULT_ALGORITHM` / `_LIMIT` / `_WINDOW` / `_BURST` | `token_bucket` / 100 / 1m / 0 | rule of the built-in `default` tenant when none is configured (reachable in header mode) |
