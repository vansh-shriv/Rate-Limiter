// Package tenant stores per-tenant rate-limit configuration and API keys in Redis,
// with an in-process cache that is invalidated over Redis pub/sub (hot reload).
package tenant

import (
	"fmt"
	"regexp"
	"time"

	"ratelimiter/internal/limiter"
)

// maxSlidingWindowLimit bounds sliding-window memory: the log holds one entry per admitted unit.
const maxSlidingWindowLimit = 100_000

// IDs end up inside Redis keys (`rl:{id}:...`), so they are restricted to a safe alphabet.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Tenant struct {
	ID        string       `json:"id"`
	Name      string       `json:"name,omitempty"`
	Rule      limiter.Rule `json:"rule"`
	Disabled  bool         `json:"disabled,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func ValidID(id string) bool { return idPattern.MatchString(id) }

// Validate checks the tenant without touching Redis. Errors wrap limiter.ErrInvalidRule.
func (t Tenant) Validate() error {
	if !ValidID(t.ID) {
		return fmt.Errorf("%w: tenant id must match %s", limiter.ErrInvalidRule, idPattern)
	}
	if err := t.Rule.Validate(); err != nil {
		return err
	}
	if t.Rule.Algorithm == limiter.SlidingWindowAlgo && t.Rule.Limit > maxSlidingWindowLimit {
		return fmt.Errorf("%w: sliding_window limit must be <= %d (memory is O(limit))", limiter.ErrInvalidRule, maxSlidingWindowLimit)
	}
	return nil
}
