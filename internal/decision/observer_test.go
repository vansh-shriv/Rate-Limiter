package decision

import (
	"context"
	"errors"
	"testing"
	"time"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type obsCall struct {
	tenant string
	algo   limiter.Algorithm
	result Result
}

type recObserver struct{ calls []obsCall }

func (r *recObserver) Observe(t string, a limiter.Algorithm, res Result, _ time.Duration) {
	r.calls = append(r.calls, obsCall{t, a, res})
}

func TestObserverClassificationAndTenantLabel(t *testing.T) {
	rule := limiter.Rule{Algorithm: limiter.TokenBucketAlgo, Limit: 5, Window: time.Second}
	cases := []struct {
		name  string
		rules rules.Provider
		lim   *fakeLimiter
		want  obsCall
	}{
		{"allowed", rules.NewStatic(rule), &fakeLimiter{res: limiter.Result{Allowed: true}}, obsCall{"acme", limiter.TokenBucketAlgo, ResultAllowed}},
		{"denied", rules.NewStatic(rule), &fakeLimiter{}, obsCall{"acme", limiter.TokenBucketAlgo, ResultDenied}},
		{"backend error keeps tenant (rule resolved)", rules.NewStatic(rule), &fakeLimiter{err: errors.New("redis")}, obsCall{"acme", limiter.TokenBucketAlgo, ResultError}},
		{"cost too high is a caller problem", rules.NewStatic(rule), &fakeLimiter{err: limiter.ErrCostTooHigh}, obsCall{"acme", limiter.TokenBucketAlgo, ResultRejected}},
		// Unresolved tenant must NOT become a label value: attacker-controlled input.
		{"unknown tenant", errRules{rules.ErrNotFound}, &fakeLimiter{}, obsCall{"unknown", "", ResultRejected}},
		{"rules backend error", errRules{errors.New("redis")}, &fakeLimiter{}, obsCall{"unknown", "", ResultError}},
	}
	for _, tc := range cases {
		o := &recObserver{}
		Checker{Limiter: tc.lim, Rules: tc.rules, Observer: o}.Check(context.Background(), "acme", "s", 1)
		if len(o.calls) != 1 || o.calls[0] != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, o.calls, tc.want)
		}
	}
}

type errRules struct{ err error }

func (e errRules) Rule(context.Context, string) (limiter.Rule, error) { return limiter.Rule{}, e.err }
