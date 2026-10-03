package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/sliding_window.lua
var slidingWindowLua string

// SlidingWindowRule allows at most Limit units in any Window-long interval.
type SlidingWindowRule struct {
	Limit  int64
	Window time.Duration
}

func (r SlidingWindowRule) validate() error {
	if r.Limit <= 0 || r.Window < time.Millisecond {
		return fmt.Errorf("%w: limit must be > 0 and window >= 1ms", ErrInvalidRule)
	}
	return nil
}

// SlidingWindow is an exact (log-based) distributed sliding window limiter.
type SlidingWindow struct {
	rdb    redis.Scripter
	script *redis.Script
}

func NewSlidingWindow(rdb redis.Scripter) *SlidingWindow {
	return &SlidingWindow{rdb: rdb, script: redis.NewScript(slidingWindowLua)}
}

// Allow admits cost units if the trailing window has room.
func (s *SlidingWindow) Allow(ctx context.Context, key string, rule SlidingWindowRule, cost int64) (Result, error) {
	if err := rule.validate(); err != nil {
		return Result{}, err
	}
	if cost <= 0 {
		return Result{}, fmt.Errorf("%w: cost must be > 0", ErrInvalidRule)
	}
	if cost > rule.Limit {
		return Result{}, ErrCostTooHigh
	}
	// Members must be unique across concurrent callers; a random id per call guarantees that.
	id := strconv.FormatUint(rand.Uint64(), 36)
	res, err := s.script.Run(ctx, s.rdb, []string{key}, rule.Limit, rule.Window.Milliseconds(), cost, id).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("sliding window script: %w", err)
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
