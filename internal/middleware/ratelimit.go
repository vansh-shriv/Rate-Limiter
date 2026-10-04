// Package middleware provides a drop-in net/http rate-limiting middleware.
package middleware

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"ratelimiter/internal/decision"
	"ratelimiter/internal/rules"
)

const DefaultTenant = "default"

type Options struct {
	Checker decision.Checker
	// TenantFunc/SubjectFunc identify whose limit applies and which bucket within it.
	// Defaults: X-Tenant-ID header (or "default"); X-API-Key header, else client IP.
	TenantFunc  func(*http.Request) string
	SubjectFunc func(*http.Request) string
	// FailOpen lets traffic through when the limiter backend errors; otherwise respond 503.
	FailOpen bool
	Log      *slog.Logger
}

// New returns middleware that enforces the tenant's rule before calling next.
func New(o Options) func(http.Handler) http.Handler {
	if o.TenantFunc == nil {
		o.TenantFunc = DefaultTenantFunc
	}
	if o.SubjectFunc == nil {
		o.SubjectFunc = DefaultSubjectFunc
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, subject := o.TenantFunc(r), o.SubjectFunc(r)
			out, err := o.Checker.Check(r.Context(), tenant, subject, 1)
			switch {
			case errors.Is(err, rules.ErrNotFound):
				http.Error(w, "unknown tenant", http.StatusForbidden)
				return
			case err != nil:
				o.Log.Error("rate limiter backend error", "tenant", tenant, "err", err, "fail_open", o.FailOpen)
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

func DefaultTenantFunc(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return DefaultTenant
}

// DefaultSubjectFunc uses the API key if present, else the TCP peer address.
// X-Forwarded-For is deliberately NOT trusted: it is client-controlled unless a trusted proxy sets it.
func DefaultSubjectFunc(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return "key:" + k
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}
