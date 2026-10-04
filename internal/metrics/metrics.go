// Package metrics exposes Prometheus instrumentation.
//
// Label cardinality is deliberately bounded: tenant only for configured tenants, route from a fixed set,
// status as a code class-able integer. Nothing derived from client input (keys, IPs, paths) is ever a label.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
)

// Decision latency matters most below ~5 ms, so the buckets are dense there.
var latencyBuckets = []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}

type Metrics struct {
	reg         *prometheus.Registry
	perTenant   bool
	decisions   *prometheus.CounterVec
	decisionDur *prometheus.HistogramVec
	cache       *prometheus.CounterVec
	httpReqs    *prometheus.CounterVec
	httpDur     *prometheus.HistogramVec
	inFlight    prometheus.Gauge
}

var _ decision.Observer = (*Metrics)(nil)

// New builds a private registry (no accidental global collectors). perTenant adds the tenant label
// to decision counters; it is bounded by the number of configured tenants.
func New(perTenant bool, version string) *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry(), perTenant: perTenant}
	m.decisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rl_decisions_total", Help: "Rate-limit decisions by tenant, algorithm and result (allowed|denied|rejected|error).",
	}, []string{"tenant", "algorithm", "result"})
	m.decisionDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "rl_decision_duration_seconds", Help: "Time to produce a decision (rule lookup + Redis script).",
		Buckets: latencyBuckets,
	}, []string{"algorithm", "result"})
	m.cache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rl_config_cache_total", Help: "Tenant/API-key cache outcomes (hit|miss|not_found|error|coalesced).",
	}, []string{"cache", "result"})
	m.httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rl_http_requests_total", Help: "HTTP requests by route and status code.",
	}, []string{"route", "code"})
	m.httpDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "rl_http_request_duration_seconds", Help: "End-to-end HTTP handling time by route (includes upstream time in gateway mode).",
		Buckets: latencyBuckets,
	}, []string{"route"})
	m.inFlight = prometheus.NewGauge(prometheus.GaugeOpts{Name: "rl_http_in_flight_requests", Help: "Requests currently being served."})
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rl_build_info", Help: "Build information."}, []string{"version"})
	build.WithLabelValues(version).Set(1)

	m.reg.MustRegister(m.decisions, m.decisionDur, m.cache, m.httpReqs, m.httpDur, m.inFlight, build,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Observe implements decision.Observer.
func (m *Metrics) Observe(tenant string, algo limiter.Algorithm, result decision.Result, took time.Duration) {
	if !m.perTenant {
		tenant = "all"
	}
	a := string(algo)
	if a == "" {
		a = "none"
	}
	m.decisions.WithLabelValues(tenant, a, string(result)).Inc()
	m.decisionDur.WithLabelValues(a, string(result)).Observe(took.Seconds())
}

// ObserveCache matches tenant.Provider.ObserveCache.
func (m *Metrics) ObserveCache(cache, result string) { m.cache.WithLabelValues(cache, result).Inc() }

// ObserveHTTP and InFlightAdd satisfy server.HTTPObserver.
func (m *Metrics) ObserveHTTP(route string, code int, took time.Duration) {
	m.httpReqs.WithLabelValues(route, strconv.Itoa(code)).Inc()
	m.httpDur.WithLabelValues(route).Observe(took.Seconds())
}

func (m *Metrics) InFlightAdd(delta float64) { m.inFlight.Add(delta) }
