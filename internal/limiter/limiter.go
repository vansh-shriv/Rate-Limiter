// Package limiter implements Redis/Lua-backed rate limiting algorithms.
package limiter

import (
	"errors"
	"time"
)

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
