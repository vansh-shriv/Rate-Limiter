package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

func TestObserveAndScrape(t *testing.T) {
	m := New(true, "test")
	m.Observe("acme", limiter.TokenBucketAlgo, decision.ResultAllowed, 300*time.Microsecond)
	m.Observe("acme", limiter.TokenBucketAlgo, decision.ResultDenied, 2*time.Millisecond)
	m.Observe("unknown", "", decision.ResultRejected, time.Microsecond)
	m.ObserveCache("tenant", "hit")
	m.ObserveHTTP("check", 429, time.Millisecond)
	m.InFlightAdd(1)

	out := scrape(t, m)
	for _, want := range []string{
		`rl_decisions_total{algorithm="token_bucket",result="allowed",tenant="acme"} 1`,
		`rl_decisions_total{algorithm="token_bucket",result="denied",tenant="acme"} 1`,
		`rl_decisions_total{algorithm="none",result="rejected",tenant="unknown"} 1`,
		`rl_decision_duration_seconds_bucket{algorithm="token_bucket",result="allowed",le="0.0005"} 1`,
		`rl_config_cache_total{cache="tenant",result="hit"} 1`,
		`rl_http_requests_total{code="429",route="check"} 1`,
		`rl_http_in_flight_requests 1`,
		`rl_build_info{version="test"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestPerTenantLabelCanBeDisabled(t *testing.T) {
	m := New(false, "test")
	m.Observe("acme", limiter.TokenBucketAlgo, decision.ResultAllowed, time.Millisecond)
	out := scrape(t, m)
	if strings.Contains(out, `tenant="acme"`) || !strings.Contains(out, `tenant="all"`) {
		t.Fatal("tenant label should be collapsed to \"all\"")
	}
}
