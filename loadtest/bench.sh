#!/usr/bin/env bash
# Reproducible benchmark: Redis + rate limiter + load generator, all on ONE Linux host over loopback.
#
#   make bench-build                       # cross-compiles bin/linux/{server,loadgen}
#   BIN=./bin/linux loadtest/bench.sh      # on Linux or inside WSL2; needs redis-server + redis-cli
#
# Env knobs: RATES="5000 10000 20000 30000"  ALGOS="token_bucket"  DURATION=20s  WARMUP=5s
#            VARIANTS="direct batched"  REDIS_PORT=6390  APP_PORT=18080  METRICS_PORT=19090  WORKERS=256  KEYS=10000
#            BURNIN=8s (discarded run per variant: fresh processes show early-life stalls on WSL2)
#            REPS=3 (repeat each point; shared machines are noisy)  PIN=1 (taskset: redis 0-1, app 2-7, loadgen 8-15)
set -euo pipefail

BIN=${BIN:-./bin/linux}
OUT=${1:-loadtest/results/$(date +%Y%m%d-%H%M%S)}
RATES=${RATES:-"5000 10000 20000 30000"}
ALGOS=${ALGOS:-token_bucket}
VARIANTS=${VARIANTS:-"direct batched"}
DURATION=${DURATION:-20s}
WARMUP=${WARMUP:-5s}
REDIS_PORT=${REDIS_PORT:-6390}
APP_PORT=${APP_PORT:-18080}
METRICS_PORT=${METRICS_PORT:-19090}
REPS=${REPS:-1}
BURNIN=${BURNIN:-8s}
PIN=${PIN:-1}
WORKERS=${WORKERS:-256}
KEYS=${KEYS:-10000}
ADMIN_TOKEN=bench-admin-token

mkdir -p "$OUT"
APP_PID=""
cleanup() { [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null || true; redis-cli -p "$REDIS_PORT" shutdown nosave 2>/dev/null || true; }
trap cleanup EXIT

{
  echo "date:      $(date -u +%FT%TZ)"
  echo "kernel:    $(uname -sr)"
  echo "cpu:       $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | xargs) x$(nproc) logical"
  echo "memory:    $(free -m | awk '/Mem:/{print $2" MB"}')"
  echo "redis:     $(redis-server --version | awk '{print $3}') (port $REDIS_PORT, no persistence)"
  echo "workers:   $WORKERS  keys: $KEYS  duration: $DURATION  warmup: $WARMUP"
} | tee "$OUT/env.txt"

# Pinning keeps our own three processes from stealing each other's cores (needs >= 16 logical CPUs; else disabled).
# PIN_* are arrays so the real process (not a shell wrapper) is what `$!` / kill refer to.
pin_cmd() { PINNED=(); if [ "$PIN" = 1 ] && [ "$(nproc)" -ge 16 ] && command -v taskset >/dev/null; then PINNED=(taskset -c "$1"); fi; }
pin() { pin_cmd "$1"; "${PINNED[@]}" "${@:2}"; }

# Refuse to run if something already listens on our ports: a stale server would silently answer instead of ours.
for port in "$APP_PORT" "$METRICS_PORT" "$REDIS_PORT"; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then echo "port $port is already in use; stop the stale process first" >&2; exit 1; fi
done

pin 0-1 redis-server --port "$REDIS_PORT" --save "" --appendonly no --daemonize yes --logfile "$OUT/redis.log" >/dev/null
for _ in $(seq 50); do redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1 && break; sleep 0.1; done

start_app() { # $1 = flushers (0 = direct)
  RL_REDIS_ADDR="127.0.0.1:$REDIS_PORT" RL_HTTP_ADDR="127.0.0.1:$APP_PORT" RL_METRICS_ADDR="127.0.0.1:$METRICS_PORT" \
  RL_ADMIN_TOKEN="$ADMIN_TOKEN" RL_BATCH_FLUSHERS="$1" RL_LOG_LEVEL=warn RL_REDIS_POOL_SIZE="${POOL:-100}" \
    pin 2-7 "$BIN/server" >"$OUT/app-$2.log" 2>&1 &
  APP_PID=$!
  for _ in $(seq 100); do curl -sf "http://127.0.0.1:$APP_PORT/readyz" >/dev/null && return; sleep 0.1; done
  echo "app failed to start; see $OUT/app-$2.log" >&2; exit 1
}
stop_app() { kill "$APP_PID" 2>/dev/null || true; wait "$APP_PID" 2>/dev/null || true; APP_PID=""; }

for variant in $VARIANTS; do
  flushers=0; [ "$variant" = batched ] && flushers=${FLUSHERS:-2}
  start_app "$flushers" "$variant"
  if [ "$BURNIN" != 0 ]; then
    pin 8-15 "$BIN/loadgen" -url "http://127.0.0.1:$APP_PORT/v1/check" -admin-url "http://127.0.0.1:$APP_PORT"       -admin-token "$ADMIN_TOKEN" -algo token_bucket -rps 5000 -duration "$BURNIN" -warmup 0s -workers "$WORKERS" -keys "$KEYS" >/dev/null || true
  fi
  for algo in $ALGOS; do
    for rps in $RATES; do
     for rep in $(seq "$REPS"); do
      redis-cli -p "$REDIS_PORT" flushall >/dev/null
      echo; echo "=== variant=$variant algo=$algo rps=$rps rep=$rep ==="
      # exit status 1 only means "SLO missed"; keep going so the whole matrix is recorded
      pin 8-15 "$BIN/loadgen" -url "http://127.0.0.1:$APP_PORT/v1/check" -admin-url "http://127.0.0.1:$APP_PORT" \
        -admin-token "$ADMIN_TOKEN" -algo "$algo" -rps "$rps" -duration "$DURATION" -warmup "$WARMUP" \
        -workers "$WORKERS" -keys "$KEYS" -slo-p99 5ms -json "$OUT/$variant-$algo-$rps-r$rep.json" | tee -a "$OUT/summary.txt" || true
     done
    done
  done
  stop_app
done
echo; echo "results in $OUT"
