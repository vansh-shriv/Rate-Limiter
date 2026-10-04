package decision

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type fakeLimiter struct {
	res  limiter.Result
	err  error
	key  string
	rule limiter.Rule
}

func (f *fakeLimiter) Allow(_ context.Context, key string, rule limiter.Rule, _ int64) (limiter.Result, error) {
	f.key, f.rule = key, rule
	return f.res, f.err
}

func TestCheckBuildsKeyFromTenantAlgoSubject(t *testing.T) {
	rule := limiter.Rule{Algorithm: limiter.SlidingWindowAlgo, Limit: 5, Window: time.Second}
	f := &fakeLimiter{}
	c := Checker{Limiter: f, Rules: rules.NewStatic(rule)}
	if _, err := c.Check(context.Background(), "acme", "ip:1.2.3.4", 1); err != nil {
		t.Fatal(err)
	}
	if f.key != "rl:{acme}:sliding_window:ip:1.2.3.4" || f.rule != rule {
		t.Fatalf("key=%q rule=%+v", f.key, f.rule)
	}
}

func TestCheckPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	c := Checker{Limiter: &fakeLimiter{err: boom}, Rules: rules.NewStatic(limiter.Rule{})}
	if _, err := c.Check(context.Background(), "t", "s", 1); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestSetHeaders(t *testing.T) {
	h := http.Header{}
	SetHeaders(h, Outcome{
		Rule:   limiter.Rule{Limit: 10},
		Result: limiter.Result{Allowed: false, Remaining: 0, RetryAfter: 1500 * time.Millisecond, ResetAfter: 2100 * time.Millisecond},
	})
	want := map[string]string{"X-Ratelimit-Limit": "10", "X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": "3", "Retry-After": "2"}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	h = http.Header{}
	SetHeaders(h, Outcome{Result: limiter.Result{Allowed: true}})
	if h.Get("Retry-After") != "" {
		t.Error("Retry-After must be absent when allowed")
	}
}
