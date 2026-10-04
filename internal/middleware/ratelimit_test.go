package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type fakeLimiter struct {
	res limiter.Result
	err error
	got string
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ limiter.Rule, _ int64) (limiter.Result, error) {
	f.got = key
	return f.res, f.err
}

func run(t *testing.T, f *fakeLimiter, failOpen bool, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	called := false
	h := New(Options{
		Checker:  decision.Checker{Limiter: f, Rules: rules.NewStatic(limiter.Rule{Algorithm: limiter.TokenBucketAlgo, Limit: 10, Window: time.Second})},
		FailOpen: failOpen,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, called
}

func TestAllowedPassesThroughWithHeaders(t *testing.T) {
	f := &fakeLimiter{res: limiter.Result{Allowed: true, Remaining: 9}}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "9.9.9.9:1234"
	req.Header.Set("X-Tenant-ID", "acme")
	rec, called := run(t, f, true, req)
	if !called || rec.Code != 200 || rec.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Fatalf("called=%v code=%d hdr=%v", called, rec.Code, rec.Header())
	}
	if f.got != "rl:{acme}:token_bucket:ip:9.9.9.9" {
		t.Fatalf("key = %q", f.got)
	}
}

func TestDeniedReturns429WithRetryAfter(t *testing.T) {
	f := &fakeLimiter{res: limiter.Result{Allowed: false, RetryAfter: 2 * time.Second}}
	rec, called := run(t, f, true, httptest.NewRequest("GET", "/", nil))
	if called || rec.Code != 429 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("called=%v code=%d hdr=%v", called, rec.Code, rec.Header())
	}
}

func TestBackendErrorFailOpenVsClosed(t *testing.T) {
	f := &fakeLimiter{err: errors.New("redis down")}
	if rec, called := run(t, f, true, httptest.NewRequest("GET", "/", nil)); !called || rec.Code != 200 {
		t.Fatalf("fail-open: called=%v code=%d", called, rec.Code)
	}
	if rec, called := run(t, f, false, httptest.NewRequest("GET", "/", nil)); called || rec.Code != 503 {
		t.Fatalf("fail-closed: called=%v code=%d", called, rec.Code)
	}
}

func TestAPIKeyBeatsIP(t *testing.T) {
	f := &fakeLimiter{res: limiter.Result{Allowed: true}}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "k1")
	run(t, f, true, req)
	if f.got != "rl:{default}:token_bucket:key:k1" {
		t.Fatalf("key = %q", f.got)
	}
}

func TestLeakyBucketDelayIsHonoredAndCancellable(t *testing.T) {
	f := &fakeLimiter{res: limiter.Result{Allowed: true, Delay: 80 * time.Millisecond}}
	start := time.Now()
	if _, called := run(t, f, true, httptest.NewRequest("GET", "/", nil)); !called {
		t.Fatal("not called")
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatal("delay not applied")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	if _, called := run(t, f, true, req); called {
		t.Fatal("cancelled request must not reach upstream")
	}
}

func TestUnknownTenantForbidden(t *testing.T) {
	f := &fakeLimiter{}
	h := New(Options{Checker: decision.Checker{Limiter: f, Rules: notFound{}}})(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 403 {
		t.Fatalf("code=%d", rec.Code)
	}
}

type notFound struct{}

func (notFound) Rule(context.Context, string) (limiter.Rule, error) {
	return limiter.Rule{}, rules.ErrNotFound
}
