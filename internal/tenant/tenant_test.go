package tenant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
)

func testRedis(tb testing.TB) *redis.Client {
	tb.Helper()
	addr := os.Getenv("RL_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		tb.Skipf("redis not available at %s: %v", addr, err)
	}
	tb.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// uniqueID gives each test its own tenant so tests don't interfere (ids are cleaned up after).
func uniqueID(t *testing.T, store *Store) string {
	id := fmt.Sprintf("t%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = store.Delete(context.Background(), id) })
	return id
}

var testRule = limiter.Rule{Algorithm: limiter.TokenBucketAlgo, Limit: 10, Window: time.Second}

func TestValidate(t *testing.T) {
	bad := []Tenant{
		{ID: "", Rule: testRule},
		{ID: "has space", Rule: testRule},
		{ID: "evil}:x", Rule: testRule},
		{ID: strings.Repeat("a", 65), Rule: testRule},
		{ID: "ok", Rule: limiter.Rule{}},
		{ID: "ok", Rule: limiter.Rule{Algorithm: limiter.SlidingWindowAlgo, Limit: maxSlidingWindowLimit + 1, Window: time.Second}},
	}
	for _, tn := range bad {
		if err := tn.Validate(); !errors.Is(err, limiter.ErrInvalidRule) {
			t.Errorf("%+v: want ErrInvalidRule, got %v", tn, err)
		}
	}
	if err := (Tenant{ID: "acme-1_x", Rule: testRule}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCRUD(t *testing.T) {
	store := NewStore(testRedis(t))
	ctx := context.Background()
	id := uniqueID(t, store)

	if _, err := store.Get(ctx, id); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	put, err := store.Put(ctx, Tenant{ID: id, Name: "Acme", Rule: testRule})
	if err != nil || put.UpdatedAt.IsZero() {
		t.Fatalf("put: %+v %v", put, err)
	}
	got, err := store.Get(ctx, id)
	if err != nil || got.Name != "Acme" || got.Rule != testRule {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, _ := store.List(ctx)
	found := false
	for _, x := range list {
		found = found || x.ID == id
	}
	if !found {
		t.Fatal("tenant missing from list")
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("want not found after delete, got %v", err)
	}
	if err := store.Delete(ctx, id); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	rdb := testRedis(t)
	store := NewStore(rdb)
	ctx := context.Background()
	id := uniqueID(t, store)
	store.Put(ctx, Tenant{ID: id, Rule: testRule})

	raw, keyID, err := store.CreateKey(ctx, id)
	if err != nil || !strings.HasPrefix(raw, "rlk_") {
		t.Fatalf("create: %q %v", raw, err)
	}
	// Only the hash is stored: the plaintext key must not appear in Redis anywhere we write.
	if rdb.Exists(ctx, "rlcfg:apikey:"+raw).Val() != 0 {
		t.Fatal("plaintext key used as Redis key")
	}
	if got, err := store.LookupKey(ctx, auth.HashKey(raw)); err != nil || got != id {
		t.Fatalf("lookup: %q %v", got, err)
	}
	if ids, _ := store.ListKeys(ctx, id); len(ids) != 1 || ids[0] != keyID {
		t.Fatalf("list keys: %v", ids)
	}
	if err := store.RevokeKey(ctx, id, "nope"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if err := store.RevokeKey(ctx, id, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupKey(ctx, auth.HashKey(raw)); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("revoked key still resolves: %v", err)
	}

	// Deleting a tenant revokes its keys too.
	raw2, _, _ := store.CreateKey(ctx, id)
	store.Delete(ctx, id)
	if _, err := store.LookupKey(ctx, auth.HashKey(raw2)); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("key survived tenant delete: %v", err)
	}
	if _, _, err := store.CreateKey(ctx, id); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("create key for missing tenant: %v", err)
	}
}

func newProvider(t *testing.T, rdb *redis.Client, ttl time.Duration) *Provider {
	t.Helper()
	p := NewProvider(rdb, NewStore(rdb), testRule, ttl, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	before := subscribers(rdb)
	go p.Watch(ctx)
	// Wait until Redis itself reports the new subscriber; a fixed sleep is racy when the machine is busy
	// (a message published before the subscription exists is simply never seen).
	deadline := time.Now().Add(5 * time.Second)
	for subscribers(rdb) <= before {
		if time.Now().After(deadline) {
			t.Fatal("pub/sub subscription never established")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p
}

func subscribers(rdb *redis.Client) int64 {
	m, err := rdb.PubSubNumSub(context.Background(), eventsChan).Result()
	if err != nil {
		return 0
	}
	return m[eventsChan]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// Two "instances" with a 1-HOUR cache TTL: a config change on A must reach B via pub/sub, not via expiry.
func TestHotReloadAcrossInstances(t *testing.T) {
	rdb := testRedis(t)
	store := NewStore(rdb)
	ctx := context.Background()
	id := uniqueID(t, store)
	store.Put(ctx, Tenant{ID: id, Rule: testRule})

	a, b := newProvider(t, rdb, time.Hour), newProvider(t, rdb, time.Hour)
	if r, err := b.Rule(ctx, id); err != nil || r.Limit != 10 { // warms B's cache
		t.Fatalf("%+v %v", r, err)
	}

	updated := testRule
	updated.Limit = 99
	if _, err := a.store.Put(ctx, Tenant{ID: id, Rule: updated}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B to see the new limit", func() bool {
		r, _ := b.Rule(ctx, id)
		return r.Limit == 99
	})

	// Disabling propagates too.
	a.store.Put(ctx, Tenant{ID: id, Rule: updated, Disabled: true})
	eventually(t, "B to see tenant disabled", func() bool {
		_, err := b.Rule(ctx, id)
		return errors.Is(err, rules.ErrDisabled)
	})
}

func TestProviderIdentifyAndRevocationPropagates(t *testing.T) {
	rdb := testRedis(t)
	store := NewStore(rdb)
	ctx := context.Background()
	id := uniqueID(t, store)
	store.Put(ctx, Tenant{ID: id, Rule: testRule})
	raw, keyID, _ := store.CreateKey(ctx, id)

	p := newProvider(t, rdb, time.Hour)
	req := httptest.NewRequest("GET", "/", nil)
	if _, err := p.Identify(req); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("no key: %v", err)
	}
	req.Header.Set("X-API-Key", "rlk_garbage")
	if _, err := p.Identify(req); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("bad key: %v", err)
	}
	req.Header.Set("X-API-Key", raw)
	got, err := p.Identify(req)
	if err != nil || got.Tenant != id || got.Subject != "key:"+keyID {
		t.Fatalf("identify: %+v %v", got, err)
	}

	store.RevokeKey(ctx, id, keyID)
	eventually(t, "revoked key to stop working", func() bool {
		_, err := p.Identify(req)
		return errors.Is(err, auth.ErrInvalidCredentials)
	})
}

func TestProviderDefaultTenantFallback(t *testing.T) {
	rdb := testRedis(t)
	p := newProvider(t, rdb, time.Minute)
	// Unless an operator creates a tenant literally named "default", it uses the configured fallback rule.
	if _, err := NewStore(rdb).Get(context.Background(), auth.DefaultTenant); err == nil {
		t.Skip("a 'default' tenant exists in this Redis")
	}
	if r, err := p.Rule(context.Background(), auth.DefaultTenant); err != nil || r != testRule {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := p.Rule(context.Background(), "no-such-tenant-xyz"); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("unknown tenant: %v", err)
	}
}
