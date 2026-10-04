"""Generates deploy/grafana/dashboards/ratelimiter.json.  Run: python deploy/gen_dashboard.py

Kept as a script so panel layout and queries stay reviewable instead of a ~900-line JSON diff.
"""
import json
import pathlib

DS = {"type": "prometheus", "uid": "prometheus"}
panels = []
_id = [0]


def panel(kind, title, x, y, w, h, exprs, unit="short", desc="", stack=False,
          thresholds=None, minv=None, maxv=None, decimals=None):
    _id[0] += 1
    targets = [{"datasource": DS, "expr": e, "legendFormat": l, "refId": chr(65 + i)}
               for i, (e, l) in enumerate(exprs)]
    defaults = {"unit": unit,
                "color": {"mode": "palette-classic" if kind == "timeseries" else "thresholds"}}
    if decimals is not None:
        defaults["decimals"] = decimals
    if minv is not None:
        defaults["min"] = minv
    if maxv is not None:
        defaults["max"] = maxv
    if thresholds:
        defaults["thresholds"] = {"mode": "absolute", "steps": thresholds}
    if kind == "timeseries":
        custom = {"lineWidth": 2, "fillOpacity": 25 if stack else 8, "showPoints": "never",
                  "stacking": {"mode": "normal" if stack else "none"}}
        if thresholds:
            custom["thresholdsStyle"] = {"mode": "line"}
        defaults["custom"] = custom
    p = {"id": _id[0], "type": kind, "title": title, "description": desc, "datasource": DS,
         "gridPos": {"x": x, "y": y, "w": w, "h": h}, "targets": targets,
         "fieldConfig": {"defaults": defaults, "overrides": []}}
    if kind == "stat":
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "value", "graphMode": "area"}
    else:
        p["options"] = {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["mean", "max"]},
                        "tooltip": {"mode": "multi", "sort": "desc"}}
    panels.append(p)


green = {"color": "green", "value": None}
amber = {"color": "orange", "value": 0.002}
red = {"color": "red", "value": 0.005}
R = "1m"
dec = f"rate(rl_decisions_total[{R}])"

# Row 1: headline stats
panel("stat", "Decisions / s", 0, 0, 5, 4, [(f"sum({dec})", "")], "reqps", decimals=0)
panel("stat", "Denied ratio", 5, 0, 5, 4,
      [(f'sum(rate(rl_decisions_total{{result="denied"}}[{R}])) / '
        f'sum(rate(rl_decisions_total{{result=~"allowed|denied"}}[{R}]))', "")],
      "percentunit", thresholds=[green, {"color": "orange", "value": 0.5}], minv=0, maxv=1, decimals=1)
panel("stat", "Decision p99", 10, 0, 5, 4,
      [(f"histogram_quantile(0.99, sum by (le) (rate(rl_decision_duration_seconds_bucket[{R}])))", "")],
      "s", desc="Rule lookup + Redis script. Target: under 5 ms.", thresholds=[green, amber, red], decimals=2)
panel("stat", "Backend errors / s", 15, 0, 4, 4,
      [(f'sum(rate(rl_decisions_total{{result="error"}}[{R}])) or vector(0)', "")],
      "reqps", thresholds=[green, {"color": "red", "value": 0.01}], decimals=2)
panel("stat", "Config cache hit ratio", 19, 0, 5, 4,
      [(f'sum(rate(rl_config_cache_total{{result="hit"}}[{R}])) / '
        f'sum(rate(rl_config_cache_total{{result=~"hit|miss|not_found|error"}}[{R}]))', "")],
      "percentunit",
      thresholds=[{"color": "red", "value": None}, {"color": "orange", "value": 0.9}, {"color": "green", "value": 0.99}],
      minv=0, maxv=1, decimals=1)

# Row 2: core behaviour
panel("timeseries", "Decisions / s by result", 0, 4, 12, 8,
      [(f"sum by (result) ({dec})", "{{result}}")], "reqps", stack=True,
      desc="allowed / denied = normal. rejected = unknown or disabled tenant, bad cost. error = Redis problem.")
panel("timeseries", "Decision latency percentiles", 12, 4, 12, 8,
      [(f"histogram_quantile({q}, sum by (le) (rate(rl_decision_duration_seconds_bucket[{R}])))", f"p{int(q * 100)}")
       for q in (0.5, 0.95, 0.99)],
      "s", thresholds=[{"color": "transparent", "value": None}, {"color": "red", "value": 0.005}],
      desc="Red line = 5 ms p99 target.")

# Row 3: who and where
panel("timeseries", "Denied / s by tenant", 0, 12, 8, 8,
      [(f'sum by (tenant) (rate(rl_decisions_total{{result="denied"}}[{R}]))', "{{tenant}}")], "reqps", stack=True)
panel("timeseries", "Decisions / s by algorithm", 8, 12, 8, 8,
      [(f"sum by (algorithm) ({dec})", "{{algorithm}}")], "reqps", stack=True)
panel("timeseries", "HTTP p99 by route", 16, 12, 8, 8,
      [(f"histogram_quantile(0.99, sum by (le, route) (rate(rl_http_request_duration_seconds_bucket[{R}])))",
        "{{route}}")], "s", desc="The gateway route includes upstream time.")

# Row 4: HTTP + cache
panel("timeseries", "HTTP requests / s by route and status", 0, 20, 12, 8,
      [(f"sum by (route, code) (rate(rl_http_requests_total[{R}]))", "{{route}} {{code}}")], "reqps", stack=True)
panel("timeseries", "Config cache outcomes / s", 12, 20, 6, 8,
      [(f"sum by (cache, result) (rate(rl_config_cache_total[{R}]))", "{{cache}} {{result}}")], "ops",
      desc="miss / not_found / error = Redis reads. coalesced = callers that shared another caller's load.")
panel("timeseries", "In-flight requests", 18, 20, 6, 8,
      [("sum(rl_http_in_flight_requests)", "in flight")], "short")

# Row 5: Redis + runtime
panel("timeseries", "Redis commands / s", 0, 28, 8, 8,
      [(f"rate(redis_commands_processed_total[{R}])", "commands/s")], "ops")
panel("timeseries", "Redis memory and clients", 8, 28, 8, 8,
      [("redis_memory_used_bytes", "memory"), ("redis_connected_clients", "clients")], "short")
panel("timeseries", "Go goroutines and heap", 16, 28, 8, 8,
      [('go_goroutines{job="ratelimiter"}', "goroutines"),
       ('go_memstats_heap_alloc_bytes{job="ratelimiter"}', "heap bytes")], "short")

dash = {
    "uid": "ratelimiter-overview", "title": "Rate Limiter Overview", "tags": ["ratelimiter"],
    "timezone": "browser", "schemaVersion": 39, "version": 1, "refresh": "5s",
    "time": {"from": "now-15m", "to": "now"}, "editable": False, "panels": panels,
}
out = pathlib.Path(__file__).parent / "grafana" / "dashboards" / "ratelimiter.json"
out.write_text(json.dumps(dash, indent=2), encoding="utf-8")
print(f"wrote {out} with {len(panels)} panels")
