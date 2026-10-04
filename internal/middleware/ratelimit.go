// Package middleware provides a drop-in net/http rate-limiting middleware.
package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/rules"
)

type Options struct {
	Checker decision.Checker
	// Identifier decides whose rule applies and which bucket. Default: auth.HeaderIdentifier (dev only).
	Identifier auth.Identifier
	// FailOpen lets traffic through when the limiter backend errors; otherwise respond 503.
	FailOpen bool
	Log      *slog.Logger
}

// New returns middleware that enforces the tenant's rule before calling next.
func New(o Options) func(http.Handler) http.Handler {
	if o.Identifier == nil {
		o.Identifier = auth.HeaderIdentifier{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := o.Identifier.Identify(r)
			var out decision.Outcome
			if err == nil {
				out, err = o.Checker.Check(r.Context(), id.Tenant, id.Subject, 1)
			}
			switch {
			case errors.Is(err, auth.ErrNoCredentials), errors.Is(err, auth.ErrInvalidCredentials):
				w.Header().Set("WWW-Authenticate", `Bearer realm="ratelimiter"`)
				http.Error(w, "valid API key required", http.StatusUnauthorized)
				return
			case errors.Is(err, rules.ErrNotFound), errors.Is(err, rules.ErrDisabled):
				http.Error(w, "tenant not allowed", http.StatusForbidden)
				return
			case err != nil:
				o.Log.Error("rate limiter backend error", "tenant", id.Tenant, "err", err, "fail_open", o.FailOpen)
				if o.FailOpen {
					next.ServeHTTP(w, r)
					return
				}
				http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
				return
			}

			decision.SetHeaders(w.Header(), out)
			if !out.Result.Allowed {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			// Leaky bucket shaping: hold the request so downstream sees a constant rate.
			if d := out.Result.Delay; d > 0 {
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-r.Context().Done():
					t.Stop()
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
