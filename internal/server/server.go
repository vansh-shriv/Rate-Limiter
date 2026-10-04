// Package server wires the HTTP surface of the service.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"ratelimiter/internal/api"
	"ratelimiter/internal/auth"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/middleware"
)

// ReadyFunc reports whether dependencies (Redis) are reachable.
type ReadyFunc func(ctx context.Context) error

type Deps struct {
	Addr            string
	Ready           ReadyFunc
	Checker         decision.Checker
	Identifier      auth.Identifier // who is calling; default auth.HeaderIdentifier (dev only)
	Admin           http.Handler    // optional: mounted at /admin/ (already authenticated)
	Upstream        *url.URL        // optional: when set, every other path is proxied here behind the rate limiter
	UpstreamTimeout time.Duration   // max wait for upstream response headers (default 30s)
	FailOpen        bool
	Log             *slog.Logger
	Metrics         HTTPObserver // optional
	// Draining, when set to true, makes /readyz return 503 so load balancers stop routing here
	// before the listener closes (graceful shutdown).
	Draining *atomic.Bool
}

func New(d Deps) *http.Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Identifier == nil {
		d.Identifier = auth.HeaderIdentifier{}
	}
	if d.Draining == nil {
		d.Draining = new(atomic.Bool)
	}
	mux := http.NewServeMux()
	route := func(name string, h http.Handler) http.Handler { return instrument(name, d.Metrics, d.Log, h) }

	// Operational endpoints are never rate limited.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if d.Draining.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := d.Ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Decision API: "should this request be allowed?" for callers that enforce limits themselves.
	mux.Handle("POST /v1/check", route("check", api.CheckHandler(d.Checker, d.Identifier, d.FailOpen, d.Log)))

	if d.Admin != nil {
		mux.Handle("/admin/", route("admin", d.Admin))
	}

	// Gateway mode: rate-limit, then reverse-proxy to the upstream.
	if d.Upstream != nil {
		limit := middleware.New(middleware.Options{Checker: d.Checker, Identifier: d.Identifier, FailOpen: d.FailOpen, Log: d.Log})
		mux.Handle("/", route("gateway", limit(newProxy(d.Upstream, d.UpstreamTimeout, d.Log))))
	}

	return &http.Server{
		Addr:              d.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: gateway responses may legitimately stream for a long time. Upstream latency is
		// bounded by UpstreamTimeout (time to response headers) instead.
	}
}

// newProxy builds the reverse proxy with an explicit transport. http.DefaultTransport keeps only 2 idle
// connections per host, which silently throttles a gateway under load.
func newProxy(target *url.URL, headerTimeout time.Duration, log *slog.Logger) http.Handler {
	if headerTimeout <= 0 {
		headerTimeout = 30 * time.Second
	}
	p := httputil.NewSingleHostReverseProxy(target)
	p.Transport = &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	director := p.Director
	p.Director = func(r *http.Request) {
		director(r)
		// The API key authenticates the caller to *us*; the upstream has no business receiving it.
		// (Authorization is left alone: upstreams commonly use it for their own auth.)
		r.Header.Del("X-API-Key")
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) {
			return // client went away; nobody to answer
		}
		log.Warn("upstream error", "path", r.URL.Path, "err", err)
		code := http.StatusBadGateway
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			code = http.StatusGatewayTimeout
		}
		writeJSON(w, code, map[string]string{"error": "upstream unavailable"})
	}
	return p
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
