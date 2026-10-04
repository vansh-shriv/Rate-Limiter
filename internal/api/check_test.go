package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type fakeLimiter struct {
	res limiter.Result
	err error
}

func (f fakeLimiter) Allow(context.Context, string, limiter.Rule, int64) (limiter.Result, error) {
	return f.res, f.err
}

func do(t *testing.T, l limiter.Limiter, failOpen bool, body string) *httptest.ResponseRecorder {
	t.Helper()
	c := decision.Checker{Limiter: l, Rules: rules.NewStatic(limiter.Rule{Algorithm: limiter.TokenBucketAlgo, Limit: 10, Window: time.Second})}
	rec := httptest.NewRecorder()
	CheckHandler(c, failOpen, slog.Default())(rec, httptest.NewRequest("POST", "/v1/check", strings.NewReader(body)))
	return rec
}

func TestCheckAllowed(t *testing.T) {
	rec := do(t, fakeLimiter{res: limiter.Result{Allowed: true, Remaining: 4}}, true, `{"key":"u1"}`)
	var resp CheckResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != 200 || !resp.Allowed || resp.Remaining != 4 || resp.Limit != 10 {
		t.Fatalf("code=%d resp=%+v", rec.Code, resp)
	}
}

func TestCheckDenied429(t *testing.T) {
	rec := do(t, fakeLimiter{res: limiter.Result{RetryAfter: 1200 * time.Millisecond}}, true, `{"tenant":"acme","key":"u1","cost":2}`)
	var resp CheckResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != 429 || resp.Allowed || resp.RetryAfterMs != 1200 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("code=%d resp=%+v hdr=%v", rec.Code, resp, rec.Header())
	}
}

func TestCheckBadInput(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `nope`,
		"missing key":   `{"tenant":"a"}`,
		"unknown field": `{"key":"k","bogus":1}`,
	} {
		if rec := do(t, fakeLimiter{}, true, body); rec.Code != 400 {
			t.Errorf("%s: code=%d", name, rec.Code)
		}
	}
}

func TestCheckLimiterValidationErrorsAre400(t *testing.T) {
	if rec := do(t, fakeLimiter{err: limiter.ErrCostTooHigh}, true, `{"key":"k","cost":999}`); rec.Code != 400 {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestCheckBackendErrorPolicy(t *testing.T) {
	l := fakeLimiter{err: errors.New("redis down")}
	open := do(t, l, true, `{"key":"k"}`)
	if open.Code != 200 || open.Header().Get("X-RateLimit-Status") != "degraded" {
		t.Fatalf("fail-open: code=%d hdr=%v", open.Code, open.Header())
	}
	if closed := do(t, l, false, `{"key":"k"}`); closed.Code != 503 {
		t.Fatalf("fail-closed: code=%d", closed.Code)
	}
}
