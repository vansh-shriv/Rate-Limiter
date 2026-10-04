package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

type allowAll struct{}

func (allowAll) Allow(context.Context, string, limiter.Rule, int64) (limiter.Result, error) {
	return limiter.Result{Allowed: true, Remaining: 1}, nil
}

func gateway(t *testing.T, upstream *url.URL, timeout time.Duration) http.Handler {
	t.Helper()
	srv := New(Deps{
		Ready:           func(context.Context) error { return nil },
		Checker:         decision.Checker{Limiter: allowAll{}, Rules: rules.NewStatic(limiter.Rule{Algorithm: limiter.TokenBucketAlgo, Limit: 1, Window: time.Second})},
		Identifier:      auth.HeaderIdentifier{},
		Upstream:        upstream,
		UpstreamTimeout: timeout,
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv.Handler
}

func TestGatewayStripsAPIKeyKeepsAuthorizationAndProxies(t *testing.T) {
	var gotKey, gotAuth, gotFwd string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth, gotFwd = r.Header.Get("X-API-Key"), r.Header.Get("Authorization"), r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	req := httptest.NewRequest("GET", "/thing", nil)
	req.RemoteAddr = "9.9.9.9:1"
	req.Header.Set("X-API-Key", "rlk_secret")
	req.Header.Set("Authorization", "Basic abc")
	rec := httptest.NewRecorder()
	gateway(t, u, time.Second).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("response not proxied: %d", rec.Code)
	}
	if gotKey != "" {
		t.Fatalf("API key leaked to upstream: %q", gotKey)
	}
	if gotAuth != "Basic abc" {
		t.Fatalf("Authorization must pass through, got %q", gotAuth)
	}
	if gotFwd != "9.9.9.9" {
		t.Fatalf("X-Forwarded-For = %q", gotFwd)
	}
}

func TestGatewayUpstreamDownIs502JSON(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close() // nothing listens here any more
	u, _ := url.Parse("http://" + addr)
	rec := httptest.NewRecorder()
	gateway(t, u, time.Second).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code=%d ct=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestGatewaySlowUpstreamIs504(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	rec := httptest.NewRecorder()
	start := time.Now()
	gateway(t, u, 100*time.Millisecond).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d", rec.Code)
	}
	if time.Since(start) > time.Second {
		t.Fatal("upstream timeout not enforced")
	}
}

func TestReadyzReportsDrainingBeforeAnythingElse(t *testing.T) {
	var draining atomic.Bool
	srv := New(Deps{Ready: func(context.Context) error { return nil }, Draining: &draining})
	get := func() (int, string) {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec.Code, rec.Body.String()
	}
	if code, _ := get(); code != 200 {
		t.Fatalf("before drain: %d", code)
	}
	draining.Store(true)
	if code, body := get(); code != 503 || body == "" {
		t.Fatalf("while draining: %d %s", code, body)
	}
	// Liveness must stay green while draining, or the orchestrator would kill the process mid-drain.
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz while draining: %d", rec.Code)
	}
}
