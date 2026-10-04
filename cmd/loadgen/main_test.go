package main

import (
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]time.Duration{.50: 50, .90: 90, .99: 99, .999: 100, 1: 100} {
		if got := percentile(s, p); got != want*time.Millisecond {
			t.Errorf("p%v = %v, want %vms", p, got, want)
		}
	}
	if percentile(nil, .99) != 0 {
		t.Error("empty input")
	}
}

// Samples outside the measured window (warmup, tail) must not influence results; transport errors are
// counted but excluded from latency percentiles.
func TestSummarizeWindowAndStatus(t *testing.T) {
	ms := time.Millisecond
	results := [][]sample{{
		{intended: 0, latency: 900 * ms, status: 200},         // warmup: ignored
		{intended: 1000 * ms, latency: 2 * ms, status: 200},   // in window
		{intended: 1500 * ms, latency: 4 * ms, status: 429},   // in window
		{intended: 1600 * ms, latency: 0, status: 0},          // transport error
		{intended: 1700 * ms, latency: 6 * ms, status: 503},   // other
		{intended: 2000 * ms, latency: 800 * ms, status: 200}, // after window: ignored
	}}
	r := summarize(results, 1000*ms, 1000*ms)
	if r.Requests != 4 || r.Allowed200 != 1 || r.Denied429 != 1 || r.OtherStatus != 1 || r.TransportErrs != 1 {
		t.Fatalf("%+v", r)
	}
	if r.MaxMs != 6 || r.P50Ms != 4 {
		t.Fatalf("latency stats wrong: %+v", r)
	}
	if r.AchievedRPS != 3 { // 3 timed responses in a 1 s window
		t.Fatalf("achieved=%v", r.AchievedRPS)
	}
}
