// Package server wires the HTTP surface of the service.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"ratelimiter/internal/api"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/middleware"
)

// ReadyFunc reports whether dependencies (Redis) are reachable.
type ReadyFunc func(ctx context.Context) error

type Deps struct {
	Addr     string
	Ready    ReadyFunc
	Checker  decision.Checker
	Upstream *url.URL // optional: when set, every other path is proxied here behind the rate limiter
	FailOpen bool
	Log      *slog.Logger
}

func New(d Deps) *http.Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	mux := http.NewServeMux()

	// Operational endpoints are never rate limited.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := d.Ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Decision API: "should this request be allowed?" for callers that enforce limits themselves.
	mux.Handle("POST /v1/check", api.CheckHandler(d.Checker, d.FailOpen, d.Log))

	// Gateway mode: rate-limit, then reverse-proxy to the upstream.
	if d.Upstream != nil {
		limit := middleware.New(middleware.Options{Checker: d.Checker, FailOpen: d.FailOpen, Log: d.Log})
		mux.Handle("/", limit(httputil.NewSingleHostReverseProxy(d.Upstream)))
	}

	return &http.Server{
		Addr:              d.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
