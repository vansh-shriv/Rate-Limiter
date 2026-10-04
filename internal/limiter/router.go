package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Algorithm selects the limiting strategy for a rule.
type Algorithm string

const (
	TokenBucketAlgo   Algorithm = "token_bucket"
	SlidingWindowAlgo Algorithm = "sliding_window"
	LeakyBucketAlgo   Algorithm = "leaky_bucket"
)

// Rule is the algorithm-agnostic, serializable limit definition (this is what per-tenant config stores).
// Semantics: "Limit units per Window".
//   - token_bucket:   refill = Limit/Window, burst capacity = Burst (default Limit)
//   - sliding_window: at most Limit in any trailing Window (Burst ignored)
//   - leaky_bucket:   drain = Limit/Window, queue capacity = Burst (default Limit)
type Rule struct {
	Algorithm Algorithm     `json:"algorithm"`
	Limit     int64         `json:"limit"`
	Window    time.Duration `json:"window"`
	Burst     int64         `json:"burst,omitempty"`
}

// Validate checks the rule without touching Redis.
func (r Rule) Validate() error {
	switch r.Algorithm {
	case TokenBucketAlgo, SlidingWindowAlgo, LeakyBucketAlgo:
	default:
		return fmt.Errorf("%w: unknown algorithm %q", ErrInvalidRule, r.Algorithm)
	}
	if r.Limit <= 0 || r.Window < time.Millisecond || r.Burst < 0 {
		return fmt.Errorf("%w: limit > 0, window >= 1ms, burst >= 0 required", ErrInvalidRule)
	}
	return nil
}

func (r Rule) burst() int64 {
	if r.Burst > 0 {
		return r.Burst
	}
	return r.Limit
}

func (r Rule) ratePerSec() float64 { return float64(r.Limit) / r.Window.Seconds() }

// Limiter is the interface the HTTP layer depends on.
type Limiter interface {
	Allow(ctx context.Context, key string, rule Rule, cost int64) (Result, error)
}

// Router dispatches a Rule to the matching algorithm. It implements Limiter.
type Router struct {
	tb *TokenBucket
	sw *SlidingWindow
	lb *LeakyBucket
}

var _ Limiter = (*Router)(nil)

func NewRouter(rdb redis.Scripter) *Router {
	return &Router{tb: NewTokenBucket(rdb), sw: NewSlidingWindow(rdb), lb: NewLeakyBucket(rdb)}
}

func (r *Router) Allow(ctx context.Context, key string, rule Rule, cost int64) (Result, error) {
	if err := rule.Validate(); err != nil {
		return Result{}, err
	}
	switch rule.Algorithm {
	case TokenBucketAlgo:
		return r.tb.Allow(ctx, key, TokenBucketRule{Capacity: rule.burst(), RefillPerSec: rule.ratePerSec()}, cost)
	case SlidingWindowAlgo:
		return r.sw.Allow(ctx, key, SlidingWindowRule{Limit: rule.Limit, Window: rule.Window}, cost)
	default: // LeakyBucketAlgo, guaranteed by Validate
		return r.lb.Allow(ctx, key, LeakyBucketRule{Capacity: rule.burst(), LeakPerSec: rule.ratePerSec()}, cost)
	}
}
