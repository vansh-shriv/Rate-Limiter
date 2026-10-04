# Benchmarks

> **Bottom line (read this first).** On the test machine below, with Redis, the service *and* the load generator all
> sharing one 8-core laptop under WSL2, the service sustained **10k req/s at p99 ≈ 3–4 ms (2 of 3 runs under 5 ms)**
> and **5k req/s at p99 ≈ 2 ms (3 of 3 runs)**. It handled **20k req/s only in the sense of accepting the offered load**:
> p99 at 20k was tens to hundreds of milliseconds, and **the "20k req/s with p99 < 5 ms" target was NOT met here.**
> We do not claim it. The sections below show why (the machine, not the algorithm, is the limit) and how to re-measure on
> real server hardware, where a single Redis thread plus pipelining should do considerably better.

## Test environment
| | |
|---|---|
| Machine | Laptop, AMD Ryzen 7 7730U (8 cores / 16 threads), 16 GB RAM (7.6 GB visible to WSL2) |
| OS | Windows 11 → WSL2 Ubuntu 26.04, kernel 6.6.87.2-microsoft-standard-WSL2 |
| Redis | 8.0.5, native in WSL2, no persistence, 1 instance |
| Topology | Redis, service and load generator on the **same host over loopback**, CPU-pinned (`taskset`): Redis 0-1, service 2-7, loadgen 8-15 |
| Background load | **Not idle**: host CPU was 52–74% busy before the runs (browser, office suite, an unrelated Docker stack). Results are noisy; every point was repeated. |
| Why not Docker Desktop Redis | A plain `redis-benchmark PING` inside the Docker Desktop VM reached only ~18k/s (p50 ≈ 2.4 ms); native WSL2 Redis reached ~42k/s. Phase 2 numbers were taken on the Docker setup and are superseded here. |
| Service config | token bucket, 10 000 distinct keys, 256 workers, metrics on, `RL_LOG_LEVEL=info`, pool 100 |

## Method
- **Open-loop load generator** (`cmd/loadgen`): requests are scheduled at a fixed rate; each latency is measured from the
  request's *intended* send time. If the system falls behind, queueing delay shows up in the percentiles instead of being omitted
  ("coordinated omission"). Closed-loop tools flatter p99 and are only used here to find the throughput ceiling.
- 5 s warmup excluded, 20 s measured, plus a discarded 8 s burn-in per server start (fresh processes showed early-life stalls on WSL2).
- A point **passes** only if p99 ≤ 5 ms with zero transport errors and zero dropped requests. We report the median of 3 runs and the
  min..max spread of p99, because single runs on this machine varied by 10× or more.
- Reproduce: `make bench-build`, then `BIN=./bin/linux REPS=3 loadtest/bench.sh` on Linux/WSL2 (needs `redis-server`, `redis-cli`); summarize with `python loadtest/summarize.py <dir>`. The script refuses to start if its ports are already in use and verifies which variant it is running.

## Results: open-loop, full stack (HTTP → auth → rule lookup → Redis script → response)
`direct` = one Redis round trip per decision. `batched` = concurrent decisions pipelined (`RL_BATCH_FLUSHERS=2`-ish; this run used 4).

| variant | target rps | achieved rps | p50 ms | p99 ms median (min..max) | p99.9 ms | runs passing SLO |
|---|---|---|---|---|---|---|
| direct | 5 000 | 5 000 | 0.98 | 2.03 (1.90..2.26) | 3.78 | 3/3 |
| batched | 5 000 | 5 000 | 1.02 | 2.01 (1.87..2.18) | 3.06 | 3/3 |
| direct | 10 000 | 10 000 | 1.19 | 3.01 (2.98..10.03) | 7.38 | 2/3 |
| batched | 10 000 | 10 000 | 1.21 | 4.06 (2.80..6.10) | 12.09 | 2/3 |
| direct | 20 000 | 20 000 | 604.90 | 2149 (907..2673) | 2156 | 0/3 |
| batched | 20 000 | 19 999 | 5.69 | 207.96 (9.67..863.81) | 217.92 | 0/3 |
| direct | 30 000 | 26 182 | 7775 | 11159 | 11173 | 0/3 (drops) |
| batched | 30 000 | 30 000 | 2638 | 2996 | 3005 | 0/3 |
| direct | 40 000 | 23 851 | 10385 | 11988 | 11996 | 0/3 (drops) |
| batched | 40 000 | 34 901 | 6077 | 7417 | 7579 | 0/3 (drops) |

Reading it: below ~10k req/s the two are identical (batching adds no latency at low load, by design). Above that, the direct
path falls over at about 15–20k, while batching keeps up to roughly 20–25k before queues build.

## Results: closed-loop throughput ceiling (`wrk`, 4 threads, same pinning)
| variant | connections | req/s | p50 | p99 |
|---|---|---|---|---|
| direct | 32 / 64 / 128 / 256 | 12.8k / 15.1k / 12.8k / 14.9k | 2.0 / 3.6 / 8.7 / 14.7 ms | 10 / 14 / 28 / 40 ms |
| batched | 32 / 64 / 128 / 256 | 15.1k / 17.9k / **24.5k** / 22.8k | 1.6 / 2.8 / 4.3 / 9.2 ms | 14 / 14 / 19 / 34 ms |

Peak: **~15k req/s direct vs ~24.5k req/s batched (+60%)**. (Latencies here are inflated: closed-loop at saturation.)

## Where the time goes (profile at 20k req/s, batched)
- CPU profile of the service: **~34% of CPU in `Syscall6`** (socket read/write), the Go HTTP server dominating; our own handler/limiter code is a small slice. WSL2 syscalls are expensive.
- Per-process CPU during the run: service ≈ 4.4 cores, **load generator ≈ 4.2 cores**, Redis ≈ 95% of its single thread. On 8 physical cores, all three compete.
- Redis script cost: `INFO commandstats` showed ~32 µs/call under load, but in an isolated pipelined micro-benchmark the same script costs **~10 µs/call**; the difference is CPU contention. Leaner script variants (string state instead of hash, dropping `TIME`) were **not faster** (11 µs, 14–16 µs), so the script was left unchanged.
- Go-level micro-benchmark against Redis (`BenchmarkTokenBucketAllow`, Docker Redis): direct 142 µs/op → batched **34 µs/op (4.1×)**.

## What changed because of this
1. **Pipelining (`limiter.Batcher`)**, default on (`RL_BATCH_FLUSHERS=2`, `RL_BATCH_MAX=128`; `0` disables). Concurrent decisions are sent as one pipeline; flushers never wait to fill a batch, so there is no added latency when idle. Atomicity is unchanged (each script is still atomic; tests prove exactly-N-allowed under concurrency through the batcher).
2. Opt-in **pprof** on the private metrics listener (`RL_PPROF_ENABLED=true`).
3. A race found while testing: a request submitted after `Batcher.Close()` could hang forever (fixed, regression test added).

## Flusher count (weak evidence)
Sweep of `RL_BATCH_FLUSHERS` ∈ {1,2,4,8} at 15k and 20k req/s, 2 runs each: 2 flushers was best (15k: p50 2.5 ms, p99 39 ms; 20k: p50 9.8 ms, p99 197 ms); the others were much worse, but run-to-run variance on this machine is as large as the differences (an earlier 4-flusher run did better than this sweep's 4-flusher run). **The default of 2 is a reasonable choice, not a proven optimum; re-tune on target hardware.**

## What we can and cannot claim
- ✅ "Atomic under concurrency": exactly N of N+M concurrent requests admitted for all three algorithms, including through the batcher.
- ✅ "≈10k req/s with p99 in the low single-digit milliseconds on a shared 8-core laptop (full stack, WSL2)"; ≈ 5k req/s with p99 ≈ 2 ms, reliably.
- ✅ "Pipelining raises throughput by ~60% (closed-loop) / ~4× at the Redis client level."
- ❌ "20k req/s with p99 < 5 ms": not demonstrated on this hardware.
- To earn it, re-run `loadtest/bench.sh` on a quiet Linux host (or two: one for the load generator), ideally with Redis on its own cores and `io-threads` enabled. Expected limiter: the single Redis thread (~10 µs/script ⇒ ~100k scripts/s in isolation); beyond that, shard tenants across Redis instances (tenant-scoped `{hash tags}` already keep each tenant's keys together).

## Honest log of mistakes made while benchmarking
- The first full matrix was **invalid**: a stale server from the previous variant kept running (I backgrounded a shell function, so `$!` was the wrapper's PID) and the "batched" variant never started. The script now checks ports are free, kills the real PID, and verifies the variant from the server's own log. Those results were discarded.
- A first flusher sweep ran every iteration with the default because of a shell quoting bug; discarded and rerun.
- Raw per-run JSON is in `loadtest/results/` (git-ignored); the tables above are the committed record.
