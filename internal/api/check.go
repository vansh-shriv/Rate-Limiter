// Package api implements the HTTP decision API.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

// CheckRequest: the tenant comes from the caller's credentials (see auth.Identifier), never from the body.
type CheckRequest struct {
	Key  string `json:"key"`  // the end-user / resource being limited, within the caller's tenant
	Cost int64  `json:"cost"` // optional, default 1
}

type CheckResponse struct {
	Allowed      bool              `json:"allowed"`
	Algorithm    limiter.Algorithm `json:"algorithm"`
	Limit        int64             `json:"limit"`
	Remaining    int64             `json:"remaining"`
	RetryAfterMs int64             `json:"retry_after_ms"`
	ResetAfterMs int64             `json:"reset_after_ms"`
	DelayMs      int64             `json:"delay_ms,omitempty"`
}

type errorBody struct {
	Error string `json:"error"`
}

// CheckHandler serves POST /v1/check. 200 = allowed, 429 = denied; both carry the full decision body
// and rate-limit headers, so callers may use either the status or the body.
func CheckHandler(c decision.Checker, ident auth.Identifier, failOpen bool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := ident.Identify(r)
		if err != nil {
			handleErr(w, err, id.Tenant, failOpen, log)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var req CheckRequest
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{"invalid JSON body: " + err.Error()})
			return
		}
		if req.Key == "" {
			writeJSON(w, http.StatusBadRequest, errorBody{"key is required"})
			return
		}
		if req.Cost == 0 {
			req.Cost = 1
		}

		out, err := c.Check(r.Context(), id.Tenant, req.Key, req.Cost)
		if err != nil {
			handleErr(w, err, id.Tenant, failOpen, log)
			return
		}

		decision.SetHeaders(w.Header(), out)
		code := http.StatusOK
		if !out.Result.Allowed {
			code = http.StatusTooManyRequests
		}
		writeJSON(w, code, CheckResponse{
			Allowed:      out.Result.Allowed,
			Algorithm:    out.Rule.Algorithm,
			Limit:        out.Rule.Limit,
			Remaining:    out.Result.Remaining,
			RetryAfterMs: out.Result.RetryAfter.Milliseconds(),
			ResetAfterMs: out.Result.ResetAfter.Milliseconds(),
			DelayMs:      out.Result.Delay.Milliseconds(),
		})
	}
}

func handleErr(w http.ResponseWriter, err error, tenant string, failOpen bool, log *slog.Logger) {
	switch {
	case errors.Is(err, auth.ErrNoCredentials), errors.Is(err, auth.ErrInvalidCredentials):
		w.Header().Set("WWW-Authenticate", `Bearer realm="ratelimiter"`)
		writeJSON(w, http.StatusUnauthorized, errorBody{"valid API key required"})
	case errors.Is(err, rules.ErrNotFound), errors.Is(err, rules.ErrDisabled):
		writeJSON(w, http.StatusForbidden, errorBody{"tenant not allowed"})
	case errors.Is(err, limiter.ErrInvalidRule), errors.Is(err, limiter.ErrCostTooHigh):
		writeJSON(w, http.StatusBadRequest, errorBody{err.Error()})
	default:
		log.Error("check failed", "tenant", tenant, "err", err, "fail_open", failOpen)
		if failOpen {
			// Explicit degraded answer: allowed, but with no quota information.
			w.Header().Set("X-RateLimit-Status", "degraded")
			writeJSON(w, http.StatusOK, CheckResponse{Allowed: true})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody{"rate limiter unavailable"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
