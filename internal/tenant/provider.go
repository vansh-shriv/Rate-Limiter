package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

// Provider is the hot-path view of tenant config. It implements rules.Provider (tenant → rule) and
// auth.Identifier (API key → tenant), backed by Store through caches kept fresh by pub/sub.
type Provider struct {
	store    *Store
	rdb      *redis.Client
	tenants  *ttlCache[Tenant]
	keys     *ttlCache[string]
	fallback limiter.Rule // rule for the built-in "default" tenant when none is configured
	log      *slog.Logger
}

var (
	_ rules.Provider  = (*Provider)(nil)
	_ auth.Identifier = (*Provider)(nil)
)

// NewProvider caches entries for ttl. The TTL is the staleness bound if a pub/sub message is ever lost.
func NewProvider(rdb *redis.Client, store *Store, fallback limiter.Rule, ttl time.Duration, log *slog.Logger) *Provider {
	return &Provider{
		store: store, rdb: rdb, fallback: fallback, log: log,
		tenants: newTTLCache[Tenant](ttl),
		keys:    newTTLCache[string](ttl),
	}
}

func (p *Provider) Rule(ctx context.Context, tenantID string) (limiter.Rule, error) {
	t, err := p.tenants.get(ctx, tenantID, func(ctx context.Context) (Tenant, error) {
		return p.store.Get(ctx, tenantID)
	})
	switch {
	case errors.Is(err, rules.ErrNotFound) && tenantID == auth.DefaultTenant:
		return p.fallback, nil
	case err != nil:
		return limiter.Rule{}, err
	case t.Disabled:
		return limiter.Rule{}, rules.ErrDisabled
	}
	return t.Rule, nil
}

// FailOpen reports the tenant's own backend-failure policy from the last known config. It never touches
// Redis (it is called precisely when Redis is failing) and ignores cache expiry.
func (p *Provider) FailOpen(tenantID string) (open, ok bool) {
	t, found := p.tenants.peek(tenantID)
	if !found || t.FailOpen == nil {
		return false, false
	}
	return *t.FailOpen, true
}

// Identify authenticates the request by API key. The bucket subject is the key's public id,
// so the raw key never reaches Redis keys or logs.
func (p *Provider) Identify(r *http.Request) (auth.Identity, error) {
	raw := auth.ExtractAPIKey(r)
	if raw == "" {
		return auth.Identity{}, auth.ErrNoCredentials
	}
	hash := auth.HashKey(raw)
	tenantID, err := p.keys.get(r.Context(), hash, func(ctx context.Context) (string, error) {
		return p.store.LookupKey(ctx, hash)
	})
	if errors.Is(err, rules.ErrNotFound) {
		return auth.Identity{}, auth.ErrInvalidCredentials
	}
	if err != nil {
		return auth.Identity{}, err
	}
	return auth.Identity{Tenant: tenantID, Subject: "key:" + auth.KeyID(hash)}, nil
}

// ObserveCache reports cache outcomes (cache = "tenant" | "apikey"). Call before serving traffic.
func (p *Provider) ObserveCache(f func(cache, result string)) {
	p.tenants.observe = func(r string) { f("tenant", r) }
	p.keys.observe = func(r string) { f("apikey", r) }
}

// Watch applies config changes made on any instance. It blocks until ctx is cancelled;
// go-redis re-subscribes automatically after connection loss (changes during the gap are
// picked up when cache entries expire).
func (p *Provider) Watch(ctx context.Context) {
	sub := p.rdb.Subscribe(ctx, eventsChan)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}
			if msg.Payload == flushAll {
				p.tenants.flush()
				p.keys.flush()
				continue
			}
			p.tenants.invalidate(msg.Payload)
		}
	}
}
