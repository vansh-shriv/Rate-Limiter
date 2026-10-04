package metrics

import (
	"strings"
	"testing"
)

func TestBreakerGauge(t *testing.T) {
	m := New(true, "test")
	m.SetBreakerOpen(true)
	if out := scrape(t, m); !strings.Contains(out, "rl_circuit_open 1") {
		t.Fatal("gauge should be 1 while open")
	}
	m.SetBreakerOpen(false)
	if out := scrape(t, m); !strings.Contains(out, "rl_circuit_open 0") {
		t.Fatal("gauge should be 0 when closed")
	}
}
