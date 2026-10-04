package tenant

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ratelimiter/internal/rules"
)

func TestCacheHitsAndExpiry(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	now := time.Now()
	c.nowFunc = func() time.Time { return now }
	var loads int
	load := func(context.Context) (int, error) { loads++; return 7, nil }

	for i := 0; i < 3; i++ {
		if v, err := c.get(context.Background(), "k", load); err != nil || v != 7 {
			t.Fatal(v, err)
		}
	}
	if loads != 1 {
		t.Fatalf("loads=%d, want 1", loads)
	}
	now = now.Add(2 * time.Minute)
	c.get(context.Background(), "k", load)
	if loads != 2 {
		t.Fatalf("expired entry should reload, loads=%d", loads)
	}
}

func TestCacheNegativeCachingButNotBackendErrors(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	var loads int
	notFound := func(context.Context) (int, error) { loads++; return 0, rules.ErrNotFound }
	for i := 0; i < 3; i++ {
		if _, err := c.get(context.Background(), "ghost", notFound); !errors.Is(err, rules.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if loads != 1 {
		t.Fatalf("not-found should be cached, loads=%d", loads)
	}

	loads = 0
	boom := func(context.Context) (int, error) { loads++; return 0, errors.New("redis down") }
	c.get(context.Background(), "k", boom)
	c.get(context.Background(), "k", boom)
	if loads != 2 {
		t.Fatalf("backend errors must not be cached, loads=%d", loads)
	}
}

func TestCacheSingleflightCollapsesConcurrentMisses(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	var loads atomic.Int64
	release := make(chan struct{})
	load := func(context.Context) (int, error) { loads.Add(1); <-release; return 1, nil }

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.get(context.Background(), "k", load) }()
	}
	time.Sleep(50 * time.Millisecond) // let them pile up behind the in-flight load
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("loads=%d, want 1", n)
	}
}

func TestCacheInvalidateAndFlush(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	var loads int
	load := func(context.Context) (int, error) { loads++; return loads, nil }
	c.get(context.Background(), "a", load)
	c.get(context.Background(), "b", load)
	c.invalidate("a")
	if v, _ := c.get(context.Background(), "a", load); v != 3 {
		t.Fatalf("a should reload, got %d", v)
	}
	c.flush()
	if v, _ := c.get(context.Background(), "b", load); v != 4 {
		t.Fatalf("b should reload after flush, got %d", v)
	}
}

func TestCacheIsBounded(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	load := func(context.Context) (int, error) { return 0, rules.ErrNotFound }
	for i := 0; i < maxCacheEntries+500; i++ { // simulates a flood of random tenant ids / keys
		c.get(context.Background(), string(rune(i))+"x"+time.Duration(i).String(), load)
	}
	c.mu.RLock()
	n := len(c.m)
	c.mu.RUnlock()
	if n > maxCacheEntries {
		t.Fatalf("cache grew to %d entries", n)
	}
}
