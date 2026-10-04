"""Summarize loadgen JSON results into a markdown table.

Usage: python loadtest/summarize.py <results-dir>

Per (variant, algorithm, target rps) it reports the MEDIAN of the repetitions and the min..max spread of p99,
because on a shared machine a single run is an anecdote. A point "passes" only if every repetition meets the
SLO (p99 <= 5 ms, zero transport errors, zero dropped requests).
"""
import collections
import json
import pathlib
import statistics
import sys

rows = collections.defaultdict(list)
for f in sorted(pathlib.Path(sys.argv[1]).glob("*.json")):
    r = json.loads(f.read_text())
    variant, algo = f.name.split("-", 1)[0], r.get("algorithm") or "token_bucket"
    rows[(variant, algo, r["target_rps"])].append(r)

print("| variant | algo | target rps | achieved rps (median) | p50 ms | p99 ms median (min..max) | p99.9 ms | reps passing SLO | errors+drops |")
print("|---|---|---|---|---|---|---|---|---|")
for (variant, algo, rps), rs in sorted(rows.items(), key=lambda kv: (kv[0][0], kv[0][1], kv[0][2])):
    med = lambda k: statistics.median(r[k] for r in rs)
    p99s = [r["p99_ms"] for r in rs]
    ok = sum(1 for r in rs if r.get("slo_met"))
    bad = sum(r["transport_errors"] + r["dropped_overload"] for r in rs)
    print(f"| {variant} | {algo} | {rps} | {med('achieved_rps'):.0f} | {med('p50_ms'):.2f} | "
          f"{statistics.median(p99s):.2f} ({min(p99s):.2f}..{max(p99s):.2f}) | {med('p99_9_ms'):.2f} | {ok}/{len(rs)} | {bad} |")
