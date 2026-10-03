# Phase 1 — Go Foundation, Config & Redis Plumbing

## Goal
A runnable service skeleton: config, pooled Redis client, health/readiness endpoints, graceful shutdown, Docker Compose with Redis.

## Steps taken
1. `go.mod` (module `ratelimiter`, Go 1.24); dependency: `go-redis/v9`.
2. `internal/config` — env-var config (`RL_*`) with defaults and validation; unit tests.
3. `internal/store` — Redis client with pool size, `MinIdleConns`, and **tight 200 ms timeouts** (a slow Redis must not stall requests; this later feeds the fail-open/closed policy in Phase 9). Pings on startup (fail fast).
4. `internal/server` — `GET /healthz` (liveness) and `GET /readyz` (readiness = Redis ping). Uses Go 1.22+ method-aware `ServeMux` patterns. Tests with `httptest`.
5. `cmd/server/main.go` — structured JSON logs (`slog`), `signal.NotifyContext`, graceful `Shutdown` with timeout.
6. `Dockerfile` — multi-stage, static binary, distroless nonroot image.
7. `docker-compose.yml` — Redis 7 (persistence off: rate-limit state is ephemeral, faster) + app, with Redis healthcheck gating app start.
8. `Makefile`, `.env.example`, `.dockerignore`.

## Ideas / trade-offs
- **Persistence off in Redis:** counters are short-lived; losing them on restart just resets limits. Revisit if durable quotas are needed.
- **Liveness vs readiness split:** orchestrators shouldn't restart the app just because Redis blipped; they should stop routing to it.
- **Pool size 100** is a starting point; tuned in Phase 8 with load data.
- Config via env only (12-factor); no config files to ship.

## Verification
- `go vet ./...` and `go test ./...` pass (config + server tests).
- `docker compose up -d --build` → `/healthz` = `{"status":"ok"}`, `/readyz` = `{"status":"ready"}`; logs show Redis connect and listen.

## Run it
```
docker compose up -d --build     # or: docker compose up -d redis && make run
curl localhost:8080/healthz
curl localhost:8080/readyz
```

## Git
```
git add .
git commit -m "feat: phase 1 - Go service skeleton, config, Redis client, health endpoints, docker-compose"
git push
```
