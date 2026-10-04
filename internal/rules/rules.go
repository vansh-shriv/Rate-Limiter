// Package rules resolves which limiter.Rule applies to a tenant.
package rules

import (
	"context"
	"errors"

	"ratelimiter/internal/limiter"
)

// ErrNotFound means the tenant is unknown and no fallback applies.
var ErrNotFound = errors.New("rules: tenant not found")

// Provider returns the rule for a tenant. Phase 6 adds a Redis-backed implementation.
type Provider interface {
	Rule(ctx context.Context, tenant string) (limiter.Rule, error)
}

// Static applies one rule to every tenant.
type Static struct{ rule limiter.Rule }

func NewStatic(r limiter.Rule) Static { return Static{rule: r} }

func (s Static) Rule(context.Context, string) (limiter.Rule, error) { return s.rule, nil }
