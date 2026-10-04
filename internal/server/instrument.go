package server

import (
	"log/slog"
	"net/http"
	"time"
)

// HTTPObserver receives per-request measurements (implemented by metrics.Metrics).
type HTTPObserver interface {
	ObserveHTTP(route string, code int, took time.Duration)
	InFlightAdd(delta float64)
}

// statusRecorder captures the response code. It implements Unwrap so http.ResponseController
// (used by httputil.ReverseProxy for flushing/streaming) still reaches the real writer.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// instrument records metrics and an access log for one logical route. Route names come from a fixed set
// chosen in New, never from the request path, so label cardinality can't be inflated by clients.
// Access logs are Debug (a line per request is too costly at 20k req/s); 5xx responses log at Warn.
func instrument(route string, obs HTTPObserver, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if obs != nil {
			obs.InFlightAdd(1)
			defer obs.InFlightAdd(-1)
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		code := rec.code
		if code == 0 {
			code = http.StatusOK
		}
		took := time.Since(start)
		if obs != nil {
			obs.ObserveHTTP(route, code, took)
		}
		level := slog.LevelDebug
		if code >= 500 {
			level = slog.LevelWarn
		}
		if log.Enabled(r.Context(), level) {
			log.LogAttrs(r.Context(), level, "request",
				slog.String("route", route), slog.String("method", r.Method), slog.String("path", r.URL.Path),
				slog.Int("status", code), slog.Duration("took", took))
		}
	})
}
