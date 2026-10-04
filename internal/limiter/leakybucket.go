package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/leaky_bucket.lua
var leakyBucketLua string

// LeakyBucketRule: a queue of Capacity units draining at LeakPerSec.
type LeakyBucketRule struct {
	Capacity   int64
	LeakPerSec float64
}

func (r LeakyBucketRule) validate() error {
	if r.Capacity <= 0 || r.LeakPerSec <= 0 {
		return fmt.Errorf("%w: capacity and leak rate must be > 0", ErrInvalidRule)
	}
	return nil
}

// LeakyBucket is a distributed leaky bucket (traffic shaper). Result.Delay tells the caller how long
// to hold the request so the downstream sees a constant rate.
type LeakyBucket struct {
	rdb    redis.Scripter
	script *redis.Script
}

func NewLeakyBucket(rdb redis.Scripter) *LeakyBucket {
	return &LeakyBucket{rdb: rdb, script: redis.NewScript(leakyBucketLua)}
}

func (l *LeakyBucket) Allow(ctx context.Context, key string, rule LeakyBucketRule, cost int64) (Result, error) {
	if err := rule.validate(); err != nil {
		return Result{}, err
	}
	if cost <= 0 {
		return Result{}, fmt.Errorf("%w: cost must be > 0", ErrInvalidRule)
	}
	if cost > rule.Capacity {
		return Result{}, ErrCostTooHigh
	}
	res, err := l.script.Run(ctx, l.rdb, []string{key}, rule.Capacity, rule.LeakPerSec, cost).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("leaky bucket script: %w", err)
	}
	if len(res) != 5 {
		return Result{}, errBadReplyShape
	}
	return Result{
		Allowed:    res[0] == 1,
		Remaining:  res[1],
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
		ResetAfter: time.Duration(res[3]) * time.Millisecond,
		Delay:      time.Duration(res[4]) * time.Millisecond,
	}, nil
}
