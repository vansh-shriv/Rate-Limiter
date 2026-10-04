package tenant

import (
	"context"
	"testing"
	"time"
)

func TestProviderFailOpenPolicyFromLastKnownConfig(t *testing.T) {
	rdb := testRedis(t)
	store := NewStore(rdb)
	ctx := context.Background()
	id := uniqueID(t, store)
	closed := false
	store.Put(ctx, Tenant{ID: id, Rule: testRule, FailOpen: &closed})

	p := newProvider(t, rdb, time.Hour)
	if _, ok := p.FailOpen(id); ok {
		t.Fatal("FailOpen must not load from Redis; nothing is cached yet")
	}
	if _, err := p.Rule(ctx, id); err != nil { // warm the cache
		t.Fatal(err)
	}
	if open, ok := p.FailOpen(id); !ok || open {
		t.Fatalf("want explicit fail-closed, got open=%v ok=%v", open, ok)
	}

	noPolicy := uniqueID(t, store)
	store.Put(ctx, Tenant{ID: noPolicy, Rule: testRule})
	p.Rule(ctx, noPolicy)
	if _, ok := p.FailOpen(noPolicy); ok {
		t.Fatal("a tenant without fail_open must defer to the global default")
	}
}
