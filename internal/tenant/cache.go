package tenant

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"ratelimiter/internal/rules"
)

const (
	maxCacheEntries = 10_000
	staleRetry      = time.Second
	loadTimeout     = 2 * time.Second
)

type entry[V any] struct {
	v   V
	err error // only ever rules.ErrNotFound (negative caching)
	exp time.Time
}

// ttlCache is a small read-through cache:
//   - hits are served under an RLock (hot path at 20k+ req/s)
//   - concurrent misses for one key share a single load (singleflight) → no thundering herd on Redis
//   - "not found" is cached briefly (negTTL) so random tenant ids / API keys can't hammer Redis
//   - backend errors are never cached, and if an expired entry exists it is served instead (stale-if-error), with
//     the next reload attempt delayed by staleRetry so a dead Redis is probed about once a second per key, not per request
//   - size is bounded: when full, expired entries are dropped, then everything is flushed
type ttlCache[V any] struct {
	mu      sync.RWMutex
	m       map[string]entry[V]
	ttl     time.Duration
	negTTL  time.Duration
	sf      singleflight.Group
	nowFunc func() time.Time
	observe func(result string) // optional: hit | miss | not_found | error | stale | coalesced
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{m: map[string]entry[V]{}, ttl: ttl, negTTL: min(ttl, 5*time.Second), nowFunc: time.Now}
}

func (c *ttlCache[V]) get(ctx context.Context, key string, load func(context.Context) (V, error)) (V, error) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if ok && c.nowFunc().Before(e.exp) {
		c.note("hit")
		return e.v, e.err
	}

	res, _, shared := c.sf.Do(key, func() (any, error) {
		// Detach from the first caller's cancellation: other callers are waiting on this result.
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		v, err := load(lctx)
		out := entry[V]{v: v, err: err}
		switch {
		case err == nil:
			out.exp = c.nowFunc().Add(c.ttl)
			c.put(key, out)
			c.note("miss")
		case errors.Is(err, rules.ErrNotFound):
			out.exp = c.nowFunc().Add(c.negTTL)
			c.put(key, out)
			c.note("not_found")
		default:
			c.note("error")
			if ok && e.err == nil { // stale-if-error: keep serving the last known good value
				out = entry[V]{v: e.v, exp: c.nowFunc().Add(staleRetry)}
				c.put(key, out)
				c.note("stale")
			}
		}
		return out, nil
	})
	if shared {
		c.note("coalesced") // waited on another goroutine's load instead of hitting Redis
	}
	r := res.(entry[V])
	return r.v, r.err
}

// peek returns the last known good value for key even if it has expired. It never loads.
func (c *ttlCache[V]) peek(key string) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[key]
	if !ok || e.err != nil {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *ttlCache[V]) note(result string) {
	if c.observe != nil {
		c.observe(result)
	}
}

func (c *ttlCache[V]) put(key string, e entry[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxCacheEntries {
		now := c.nowFunc()
		for k, old := range c.m {
			if !now.Before(old.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxCacheEntries {
			clear(c.m)
		}
	}
	c.m[key] = e
}

func (c *ttlCache[V]) invalidate(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *ttlCache[V]) flush() {
	c.mu.Lock()
	clear(c.m)
	c.mu.Unlock()
}
