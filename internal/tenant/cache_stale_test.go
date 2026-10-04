package tenant

import (
	"context"
	"errors"
	"testing"
	"time"

	"ratelimiter/internal/rules"
)

func TestCacheServesStaleOnBackendErrorAndBacksOffRetries(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	now := time.Now()
	c.nowFunc = func() time.Time { return now }
	if v, _ := c.get(context.Background(), "k", func(context.Context) (int, error) { return 7, nil }); v != 7 {
		t.Fatal(v)
	}
	now = now.Add(2 * time.Minute) // entry expired, and Redis is now down

	var loads int
	down := func(context.Context) (int, error) { loads++; return 0, errors.New("redis down") }
	got := map[string]int{}
	c.observe = func(r string) { got[r]++ }
	for i := 0; i < 5; i++ {
		v, err := c.get(context.Background(), "k", down)
		if err != nil || v != 7 {
			t.Fatalf("stale value must be served during an outage: %d %v", v, err)
		}
	}
	if loads != 1 {
		t.Fatalf("reload must be rate-limited during an outage, loads=%d", loads)
	}
	now = now.Add(staleRetry + time.Millisecond) // retry window elapsed: probe Redis once more
	c.get(context.Background(), "k", down)
	if loads != 2 || got["stale"] != 2 {
		t.Fatalf("loads=%d observed=%v", loads, got)
	}

	// Redis recovers: the next reload after the backoff window replaces the stale value.
	now = now.Add(staleRetry + time.Millisecond)
	if v, err := c.get(context.Background(), "k", func(context.Context) (int, error) { return 9, nil }); err != nil || v != 9 {
		t.Fatalf("recovered value expected, got %d %v", v, err)
	}
}

func TestCacheNoStaleWhenNeverLoaded(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	if _, err := c.get(context.Background(), "new", func(context.Context) (int, error) { return 0, errors.New("redis down") }); err == nil {
		t.Fatal("with nothing cached the error must surface")
	}
}

func TestCachePeekIgnoresExpiryButNotNegatives(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	now := time.Now()
	c.nowFunc = func() time.Time { return now }
	c.get(context.Background(), "k", func(context.Context) (int, error) { return 5, nil })
	c.get(context.Background(), "ghost", func(context.Context) (int, error) { return 0, rules.ErrNotFound })
	now = now.Add(time.Hour)
	if v, ok := c.peek("k"); !ok || v != 5 {
		t.Fatalf("peek should return an expired good value: %d %v", v, ok)
	}
	if _, ok := c.peek("ghost"); ok {
		t.Fatal("peek must not return negative entries")
	}
	if _, ok := c.peek("absent"); ok {
		t.Fatal("peek on an absent key")
	}
}
