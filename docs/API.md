# API Reference

## Operational
| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness (Redis ping); 503 if down |
Never rate limited.

## Decision API — `POST /v1/check`
Ask "may this request proceed?" without proxying anything.
```
POST /v1/check
{"tenant": "acme", "key": "user42", "cost": 1}      # tenant defaults to "default", cost to 1
```
| Status | Meaning |
|---|---|
| 200 | allowed |
| 429 | denied (body + `Retry-After` tell when to retry) |
| 400 | bad JSON, missing `key`, unknown field, `cost` > capacity, invalid rule |
| 404 | unknown tenant |
| 503 | backend (Redis) error and fail-closed |
200/429 body:
```json
{"allowed":true,"algorithm":"token_bucket","limit":5,"remaining":4,"retry_after_ms":0,"reset_after_ms":2000,"delay_ms":0}
```
`delay_ms` (leaky bucket only) = how long the caller should hold the request. With fail-open and a backend error: `200`, `allowed:true`, header `X-RateLimit-Status: degraded`.

## Gateway mode — everything else
Set `RL_UPSTREAM_URL`; all other paths are rate limited, then reverse-proxied.
- **Tenant:** `X-Tenant-ID` header (default `default`). *(Phase 6 replaces this with API-key → tenant lookup; a client-supplied tenant header is not authentication.)*
- **Subject (bucket):** `X-API-Key` header, else TCP peer IP. `X-Forwarded-For` is **not** trusted.
- **Headers:** `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` (seconds), and on 429 `Retry-After` (seconds, ≥1).
- Leaky bucket: admitted requests are held for `Delay` (cancelled if the client disconnects).

## Configuration (env)
| Var | Default | |
|---|---|---|
| `RL_HTTP_ADDR` | `:8080` | |
| `RL_REDIS_ADDR` / `_PASSWORD` / `_DB` / `_POOL_SIZE` / `_TIMEOUT` | `localhost:6379` / – / 0 / 100 / 200ms | |
| `RL_UPSTREAM_URL` | – | enables gateway mode |
| `RL_FAIL_OPEN` | `true` | allow traffic if Redis errors |
| `RL_DEFAULT_ALGORITHM` | `token_bucket` | `token_bucket` \| `sliding_window` \| `leaky_bucket` |
| `RL_DEFAULT_LIMIT` / `_WINDOW` / `_BURST` | 100 / 1m / 0 (=limit) | |
