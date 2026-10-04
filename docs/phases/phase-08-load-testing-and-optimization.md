# Phase 8 — Load Testing & Optimization

## Goal
Measure the system honestly, find the bottleneck, and optimize what the data supports. Full numbers and caveats: [../BENCHMARKS.md](../BENCHMARKS.md).

## What was added
```
cmd/loadgen/                 open-loop load generator (coordinated-omission-safe latency from intended send time)
loadtest/bench.sh            reproducible benchmark: Redis + service + loadgen on one Linux host, CPU-pinned, REPS, burn-in, SLO verdict
loadtest/summarize.py        median + min..max spread per point -> markdown table
internal/limiter/batcher.go  redis.Scripter that pipelines concurrent EVALSHA calls (default on)
Makefile                     bench-build, bench
config                       RL_BATCH_FLUSHERS (default 2), RL_BATCH_MAX (128), RL_PPROF_ENABLED (false)
```

## Process (what actually happened)
1. **Environment.** WSL2 Ubuntu was registered but broken (missing disk image). With approval, it was reinstalled by the user; Redis 8.0.5 was installed in it as root. Docker Desktop Redis was rejected as a benchmark target (PING ≈ 18k/s there vs ≈ 42k/s native).
2. **Tool choice.** An open-loop generator was written because closed-loop tools omit queueing delay and make p99 meaningless. `wrk` (closed-loop) was added only to find the throughput ceiling.
3. **First matrix: invalid.** A stale-server bug in my script meant the batched variant never ran. Caught by noticing a server still alive after the run; fixed and made self-verifying (ports, PID, variant check).
4. **Valid matrix** (3 reps × 5 rates × 2 variants). Passing: 5k req/s (p99 ≈ 2 ms). Borderline: 10k. Failing: ≥ 20k. Batching ≥ direct everywhere.
5. **Profiling** (pprof): the service burns ~34% CPU in socket syscalls; the load generator and the service each use 4+ cores; Redis's single thread is near saturation. WSL2 + a shared 8-core laptop is the limit.
6. **Hypothesis tested and rejected:** "the Lua script is too heavy." Isolated, it costs ~10 µs/call; leaner variants were no faster. Script unchanged.
7. **Flusher sweep:** inconclusive (variance ≥ effect size); default 2.

## Decisions
- **Batching is on by default.** It never made things worse, adds no latency at low load (flushers don't wait for a batch to fill), and gave +60% closed-loop throughput. `RL_BATCH_FLUSHERS=0` restores one round trip per decision.
- **No claim of 20k req/s @ p99 < 5 ms.** Not demonstrated on this hardware; documented what is, and how to re-measure.
- **pprof is opt-in** and only on the private metrics listener.

## Bugs found by tests/benchmarks in this phase
- `Batcher.EvalSha` after `Close()` could block forever (buffered queue accepted the send; no flusher left to answer). Found when the full suite hung; fixed by checking `stop` first and releasing waiters on `stop`; regression test runs 200 iterations because the bug depended on `select`'s random choice.
- Benchmark script: stale process + wrong PID (see above); shell-quoting bug in the sweep.

## Lessons
- A benchmark you haven't verified is measuring something else. Make the harness assert what it is running.
- Report spread, not one number: on this machine identical runs differed by 10× in p99.
- Profile before optimizing: the obvious suspect (the Lua script) was innocent.

## Ideas not done
- Batch-size histogram metric to tune `RL_BATCH_MAX`.
- Compare Redis `io-threads`, and Redis Functions (`FCALL`) vs `EVALSHA`.
- Cheaper load generation (fasthttp) so the generator uses less CPU than the service.
- k6 scenarios (tool not installed here; the Go generator covers the same ground).
- Run on a quiet dedicated Linux host to attempt the 20k @ 5 ms claim.

## Tests
`loadgen`: nearest-rank percentile, window/status accounting. `limiter`: batcher atomicity (exactly 100 of 2000), per-caller result routing under shared pipelines, context cancel, close, **no hang after close**. `config`: batching defaults/disable/validation. Full suite passes.

## Git
```
git add .
git commit -m "feat: phase 8 - open-loop load generator, benchmark harness, Redis pipelining and honest benchmark results"
git push
```
