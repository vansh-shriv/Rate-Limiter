// Package decision is the single entry point both the /v1/check API and the middleware use
// to turn (tenant, subject, cost) into a rate-limit decision.
package decision

import (
	"context"
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

type Checker struct {
	Limiter limiter.Limiter
	Rules   rules.Provider
}

func (c Checker) Check(ctx context.Context, tenant, subject string, cost int64) (Outcome, error) {
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
