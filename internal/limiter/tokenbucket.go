// Package limiter implements Redis/Lua-backed rate limiting algorithms.
package limiter

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketLua string

var (
	ErrInvalidRule   = errors.New("limiter: invalid rule")
	ErrCostTooHigh   = errors.New("limiter: cost exceeds bucket capacity")
	errBadReplyShape = errors.New("limiter: unexpected script reply")
)

// Result is the outcome of a single rate-limit decision.
type Result struct {
	Allowed    bool
	Remaining  int64         // whole tokens/slots left after this decision
	RetryAfter time.Duration // 0 when allowed
	ResetAfter time.Duration // time until the limiter is back to full
}

// TokenBucketRule configures a bucket: bursts up to Capacity, refilling at RefillPerSec.
type TokenBucketRule struct {
	Capacity     int64
	RefillPerSec float64
}

func (r TokenBucketRule) validate() error {
	if r.Capacity <= 0 || r.RefillPerSec <= 0 {
		return fmt.Errorf("%w: capacity and refill rate must be > 0", ErrInvalidRule)
	}
	return nil
}

// TokenBucket is a distributed token bucket; all state lives in Redis.
type TokenBucket struct {
	rdb    redis.Scripter
	script *redis.Script
}

func NewTokenBucket(rdb redis.Scripter) *TokenBucket {
	return &TokenBucket{rdb: rdb, script: redis.NewScript(tokenBucketLua)}
}

// Allow tries to take cost tokens from the bucket at key.
// Script.Run uses EVALSHA and transparently falls back to EVAL on NOSCRIPT.
func (t *TokenBucket) Allow(ctx context.Context, key string, rule TokenBucketRule, cost int64) (Result, error) {
	if err := rule.validate(); err != nil {
		return Result{}, err
	}
	if cost <= 0 {
		return Result{}, fmt.Errorf("%w: cost must be > 0", ErrInvalidRule)
	}
	if cost > rule.Capacity {
		return Result{}, ErrCostTooHigh
	}
	res, err := t.script.Run(ctx, t.rdb, []string{key}, rule.Capacity, rule.RefillPerSec, cost).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("token bucket script: %w", err)
	}
	if len(res) != 4 {
		return Result{}, errBadReplyShape
	}
	return Result{
		Allowed:    res[0] == 1,
		Remaining:  res[1],
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
		ResetAfter: time.Duration(res[3]) * time.Millisecond,
	}, nil
}
