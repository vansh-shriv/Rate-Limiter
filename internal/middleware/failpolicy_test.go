package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type tenantPolicy map[string]bool

func (p tenantPolicy) FailOpen(t string) (bool, bool) { v, ok := p[t]; return v, ok }

// A strict tenant is rejected during an outage even though the global default is fail-open (and vice versa).
func TestPerTenantFailPolicyOverridesGlobal(t *testing.T) {
	for _, tc := range []struct {
		tenant string
		global bool
		want   int
	}{
		{"strict", true, 503},
		{"lenient", false, 200},
		{"plain", true, 200},
		{"plain", false, 503},
	} {
		h := New(Options{
			Identifier: fixedID{id: auth.Identity{Tenant: tc.tenant, Subject: "s"}},
			Checker: decision.Checker{
				Limiter: &fakeLimiter{err: errors.New("redis down")}, Rules: rules.NewStatic(limiter.Rule{}),
				Policy: tenantPolicy{"strict": false, "lenient": true},
			},
			FailOpen: tc.global,
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != tc.want {
			t.Errorf("tenant=%s global=%v: code=%d want %d", tc.tenant, tc.global, rec.Code, tc.want)
		}
	}
}
