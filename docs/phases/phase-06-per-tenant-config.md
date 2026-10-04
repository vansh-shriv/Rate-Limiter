# Phase 6 — Per-Tenant Configuration

## Goal
Each tenant gets its own algorithm/limit/window/burst, managed at runtime through an admin API, applied on every instance without restart, and tied to API keys so the tenant can't be spoofed. Reference: [../API.md](../API.md).

## New packages
```
internal/auth/      Identity + Identifier interface, API-key extraction/hashing, HeaderIdentifier (dev)
internal/tenant/    Tenant model+validation, Redis Store, ttlCache, Provider (rules.Provider + auth.Identifier), pub/sub Watch
internal/admin/     /admin/v1 handlers behind a bearer token
internal/limiter/rule_json.go   Rule <-> JSON with human windows ("1m")
```
Changed: `middleware` and `api` now depend on `auth.Identifier` instead of header funcs; `server.Deps` gained `Identifier` and `Admin`; config gained `RL_ADMIN_TOKEN`, `RL_AUTH_MODE`, `RL_TENANT_CACHE_TTL`.

## Redis data model
| Key | Type | Content |
|---|---|---|
| `rlcfg:{id}:doc` | string | tenant JSON |
| `rlcfg:tenants` | set | tenant ids |
| `rlcfg:{id}:keys` | hash | keyID → key hash |
| `rlcfg:apikey:<hash>` | string | tenant id (the lookup index) |
| `rlcfg:events` | pub/sub | payload = tenant id, or `*` = flush everything |

## Request flow (data plane)
`API key → hash → (cache|Redis) tenant id → (cache|Redis) tenant rule → limiter script`.
Two cache lookups per request, both RLock hits in steady state, so config adds **no Redis round trips** to the hot path.

## Decisions (details in DECISIONS.md ADR-006..010)
- **Hot reload via pub/sub** with a TTL safety net. Verified by a test with a *1-hour* TTL: a change on instance A reaches instance B in milliseconds, so it can't be passing because of expiry.
- **Keys hashed at rest**, shown once; bucket subject is the public key id; revocation and tenant deletion propagate by `*` flush.
- **Tenant from credentials** (apikey mode default). `/v1/check` rejects a `tenant` field. `header` mode is dev-only with a startup warning.
- **Cache hardening:** singleflight, 5 s negative caching, no caching of backend errors, 10k-entry cap.
- **Validation as a security control:** tenant ids restricted to `[a-zA-Z0-9_-]{1,64}` because they are embedded in Redis keys (`rl:{id}:...`) — stops key injection; sliding-window `limit` ≤ 100 000 to bound per-key memory.
- **Admin API** off unless `RL_ADMIN_TOKEN` set; constant-time compare; `Cache-Control: no-store`.
- The built-in `default` tenant falls back to the `RL_DEFAULT_*` rule when no tenant named `default` exists (reachable only in header mode).

## Known limitations / ideas
- A cache load racing an invalidation can briefly re-cache the old value; bounded by the TTL (30 s). Acceptable for rate-limit config.
- Pub/sub messages sent while an instance is reconnecting are missed; same TTL bound.
- Limits are **per API key** within a tenant. A *tenant-wide shared quota* (all keys drain one bucket) is a possible extension.
- `RL_DEFAULT_*` env still exists for the fallback rule; Phase 9 may drop it.
- Redis Cluster: `{id}` hash tags keep a tenant's doc+keys in one slot, but the `apikey:<hash>` index is a different slot, so the multi-key transactions would need splitting under Cluster.
- Admin auth is one shared token (ADR-010).

## Tests
- `limiter`: Rule JSON round-trip, bad window → `ErrInvalidRule`.
- `auth`: key extraction (header precedence, bearer), header identifier, raw key never in subject.
- `tenant` (unit): cache hit/expiry, negative caching vs backend errors, singleflight (50 concurrent → 1 load), invalidate/flush, size bound.
- `tenant` (Redis): validation table; CRUD; key lifecycle (hash-only storage, revoke, delete-tenant revokes keys); **hot reload across two providers**; disable propagation; revocation propagation; default-tenant fallback.
- `admin` (Redis): token required (empty/wrong/prefix/no-Bearer all 401); full tenant+key flow; validation → 400; unknown tenant → 404.
- `middleware`: identifier errors (401 never fail-open; backend error follows fail-open/closed), disabled tenant → 403.
- **End-to-end (Docker):** no key → 401; admin w/o token → 401; create tenant (sliding window 3/min) + key → `200 200 200 429 429`; PUT new rule (token bucket 100/min) → effective on the next request (`X-RateLimit-Limit: 100`); `/v1/check` works with key; disable → 403; revoke → 401; Redis contains **no plaintext key**.

## Gotchas hit
- `go get golang.org/x/sync@latest` pulled v0.23.0, which requires Go 1.26 and silently rewrote `go.mod` to `go 1.26.0` (and tried to download a toolchain). Pinned `x/sync v0.12.0` and restored `go 1.24`. Lesson: always check `go.mod` after `go get`.

## Git
```
git add .
git commit -m "feat: phase 6 - per-tenant rules, API keys, admin API and pub/sub hot reload"
git push
```
