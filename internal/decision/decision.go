// Package decision is the single entry point both the /v1/check API and the middleware use
// to turn (tenant, subject, cost) into a rate-limit decision.
package decision

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type Outcome struct {
	Rule   limiter.Rule
	Result limiter.Result
}

// Result classifies a decision for metrics.
type Result string

const (
	ResultAllowed  Result = "allowed"
	ResultDenied   Result = "denied"
	ResultRejected Result = "rejected" // unknown/disabled tenant or invalid rule/cost: a caller problem
	ResultError    Result = "error"    // backend failure
)

// Observer receives one call per decision. tenant is "unknown" unless the tenant's rule resolved,
// which keeps metric label cardinality bounded by configured tenants (never by attacker input).
type Observer interface {
	Observe(tenant string, algorithm limiter.Algorithm, result Result, took time.Duration)
}

// Policy supplies per-tenant overrides of the backend-failure policy. It must not block or call Redis.
type Policy interface {
	FailOpen(tenant string) (open, ok bool)
}

type Checker struct {
	Limiter  limiter.Limiter
	Rules    rules.Provider
	Observer Observer // optional
	Policy   Policy   // optional
}

// ShouldFailOpen decides what to do when the backend errored: the tenant's own setting if it has one,
// otherwise the global default.
func (c Checker) ShouldFailOpen(tenant string, global bool) bool {
	if c.Policy != nil {
		if open, ok := c.Policy.FailOpen(tenant); ok {
			return open
		}
	}
	return global
}

func (c Checker) Check(ctx context.Context, tenant, subject string, cost int64) (Outcome, error) {
	start := time.Now()
	out, err := c.check(ctx, tenant, subject, cost)
	if c.Observer != nil {
		label := "unknown"
		if out.Rule.Algorithm != "" { // rule resolved => tenant is real
			label = tenant
		}
		c.Observer.Observe(label, out.Rule.Algorithm, classify(out, err), time.Since(start))
	}
	return out, err
}

func (c Checker) check(ctx context.Context, tenant, subject string, cost int64) (Outcome, error) {
	rule, err := c.Rules.Rule(ctx, tenant)
	if err != nil {
		return Outcome{}, err
	}
	res, err := c.Limiter.Allow(ctx, limiter.Key(tenant, rule.Algorithm, subject), rule, cost)
	if err != nil {
		return Outcome{Rule: rule}, err
	}
	return Outcome{Rule: rule, Result: res}, nil
}

func classify(o Outcome, err error) Result {
	switch {
	case err == nil && o.Result.Allowed:
		return ResultAllowed
	case err == nil:
		return ResultDenied
	case errors.Is(err, rules.ErrNotFound), errors.Is(err, rules.ErrDisabled),
		errors.Is(err, limiter.ErrInvalidRule), errors.Is(err, limiter.ErrCostTooHigh):
		return ResultRejected
	default:
		return ResultError
	}
}

// SetHeaders writes the conventional rate-limit headers.
func SetHeaders(h http.Header, o Outcome) {
	h.Set("X-RateLimit-Limit", strconv.FormatInt(o.Rule.Limit, 10))
	h.Set("X-RateLimit-Remaining", strconv.FormatInt(o.Result.Remaining, 10))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(ceilSeconds(o.Result.ResetAfter), 10))
	if !o.Result.Allowed {
		h.Set("Retry-After", strconv.FormatInt(max(1, ceilSeconds(o.Result.RetryAfter)), 10))
	}
}

func ceilSeconds(d time.Duration) int64 {
	return int64((d + time.Second - 1) / time.Second)
}
