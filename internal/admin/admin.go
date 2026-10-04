// Package admin implements the tenant-management HTTP API (protected by a static bearer token).
package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
	"ratelimiter/internal/tenant"
)

type Handler struct {
	store *tenant.Store
	token [32]byte // SHA-256 of the admin token: fixed-length compare leaks nothing about its length
	log   *slog.Logger
}

// New returns the admin handler, rooted at /admin/v1/. Callers must not mount it with an empty token.
func New(store *tenant.Store, token string, log *slog.Logger) http.Handler {
	h := &Handler{store: store, token: sha256.Sum256([]byte(token)), log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/tenants", h.list)
	mux.HandleFunc("PUT /admin/v1/tenants/{id}", h.put)
	mux.HandleFunc("GET /admin/v1/tenants/{id}", h.get)
	mux.HandleFunc("DELETE /admin/v1/tenants/{id}", h.delete)
	mux.HandleFunc("GET /admin/v1/tenants/{id}/keys", h.listKeys)
	mux.HandleFunc("POST /admin/v1/tenants/{id}/keys", h.createKey)
	mux.HandleFunc("DELETE /admin/v1/tenants/{id}/keys/{keyID}", h.revokeKey)
	return h.authed(mux)
}

func (h *Handler) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(sum[:], h.token[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			writeJSON(w, http.StatusUnauthorized, errBody{"admin token required"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type putRequest struct {
	Name     string       `json:"name"`
	Rule     limiter.Rule `json:"rule"`
	Disabled bool         `json:"disabled"`
	FailOpen *bool        `json:"fail_open"`
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req putRequest
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{"invalid JSON body: " + err.Error()})
		return
	}
	t, err := h.store.Put(r.Context(), tenant.Tenant{ID: r.PathValue("id"), Name: req.Name, Rule: req.Rule, Disabled: req.Disabled, FailOpen: req.FailOpen})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	t, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ts, err := h.store.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": ts})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	ids, err := h.store.ListKeys(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key_ids": ids})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	raw, keyID, err := h.store.CreateKey(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	// The only time the plaintext key is ever available.
	writeJSON(w, http.StatusCreated, map[string]string{"key": raw, "key_id": keyID})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokeKey(r.Context(), r.PathValue("id"), r.PathValue("keyID")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type errBody struct {
	Error string `json:"error"`
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, limiter.ErrInvalidRule):
		writeJSON(w, http.StatusBadRequest, errBody{err.Error()})
	case errors.Is(err, rules.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody{"tenant not found"})
	case errors.Is(err, tenant.ErrKeyNotFound):
		writeJSON(w, http.StatusNotFound, errBody{"api key not found"})
	default:
		h.log.Error("admin request failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody{"internal error"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
