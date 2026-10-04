# Phase 5 — HTTP API & Middleware

## Goal
Make the limiters usable: a decision API, a drop-in `net/http` middleware, and a reverse-proxy gateway mode. Full reference in [../API.md](../API.md).

## Package layout (new)
```
internal/limiter/key.go        Key(tenant, algo, subject) -> "rl:{tenant}:algo:subject"
internal/rules/                Provider interface (+ Static impl); Phase 6 adds Redis-backed tenants
internal/decision/             Checker (rule lookup + limiter call) and SetHeaders — shared by API & middleware
internal/middleware/           RateLimit middleware (tenant/subject funcs, fail-open, leaky delay)
internal/api/                  POST /v1/check
internal/server/               wiring; Deps struct replaces positional args
```

## Key decisions
1. **One decision path.** `decision.Checker` is used by both the API and the middleware so behavior (key scheme, rule resolution, headers) can't drift.
2. **`rules.Provider` seam.** Phase 5 uses one static default rule from env; Phase 6 swaps in per-tenant rules with no change to callers.
3. **Gateway mode = middleware + `httputil.ReverseProxy`**, enabled by `RL_UPSTREAM_URL`. That is the "drop-in" story: put it in front of any HTTP service.
4. **429 on deny for `/v1/check`**, with the full body on both 200 and 429: simple clients can use the status, richer ones the body.
5. **Failure policy** (`RL_FAIL_OPEN`, default true): Redis errors either pass traffic (logged; `/v1/check` marks `X-RateLimit-Status: degraded`) or return 503. Per-tenant policy comes in Phase 9. Rationale for default open: a limiter outage should not become a full outage of the protected service.
6. **Security notes:** `X-Forwarded-For` ignored (client-spoofable → trivial limit bypass); `X-Tenant-ID` is untrusted until Phase 6 ties tenant to API key; `/v1/check` body capped at 1 MiB and decoded strictly (`DisallowUnknownFields`).
7. **Leaky bucket in middleware** sleeps `Delay` using a timer + `ctx.Done()`, so disconnected clients don't hold goroutines.
8. Operational endpoints are registered on separate mux routes → never rate limited.

## Tests
- Unit (fake limiter, no Redis): middleware (pass-through + headers, 429 + Retry-After, fail-open/closed, API key vs IP, leaky delay + cancellation, unknown tenant → 403), API (allowed, 429, bad input ×3, validation → 400, backend error policy), decision (key building, error propagation, headers), server routing.
- **End-to-end via Docker Compose** (app → whoami upstream, limit 5 / 10 s):
  - 7 requests → `200 200 200 200 200 429 429`
  - 429 carries `Retry-After: 2`, `X-RateLimit-Limit: 5`, `Remaining: 0`, `Reset: 10`
  - another tenant (`X-Tenant-ID: acme`) gets an independent bucket → 200
  - `/v1/check` returns `remaining` 4 → 3; missing key → 400; `/healthz` unaffected.

## Compose changes
Added `traefik/whoami` as a demo upstream; the app now proxies to it with demo limits (5 / 10 s). Real limits come from config/tenants.

## Git
```
git add .
git commit -m "feat: phase 5 - /v1/check decision API, rate-limit middleware and reverse-proxy gateway mode"
git push
```
