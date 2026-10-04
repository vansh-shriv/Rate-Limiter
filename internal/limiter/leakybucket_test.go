package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeakyBucketSpacesRequests(t *testing.T) {
	rdb := testRedis(t)
	lb := NewLeakyBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := LeakyBucketRule{Capacity: 3, LeakPerSec: 10} // one unit / 100ms

	// Back-to-back requests get delays ~0, ~100, ~200 ms: no burst, constant output rate.
	for i := 0; i < 3; i++ {
		r, err := lb.Allow(ctx, key, rule, 1)
		if err != nil || !r.Allowed {
			t.Fatalf("req %d: %+v err=%v", i, r, err)
		}
		want := time.Duration(i) * 100 * time.Millisecond
		if r.Delay > want || r.Delay < want-30*time.Millisecond {
			t.Fatalf("req %d: delay %v, want ~%v", i, r.Delay, want)
		}
	}
	// Queue is full; next request is rejected and told when there will be room (~100ms).
	r, _ := lb.Allow(ctx, key, rule, 1)
	if r.Allowed || r.Delay != 0 || r.RetryAfter < 50*time.Millisecond || r.RetryAfter > 100*time.Millisecond {
		t.Fatalf("expected rejection with retry ~100ms: %+v", r)
	}
}

func TestLeakyBucketDrains(t *testing.T) {
	rdb := testRedis(t)
	lb := NewLeakyBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := LeakyBucketRule{Capacity: 2, LeakPerSec: 20} // 50ms / unit

	lb.Allow(ctx, key, rule, 1)
	lb.Allow(ctx, key, rule, 1)
	if r, _ := lb.Allow(ctx, key, rule, 1); r.Allowed {
		t.Fatal("queue should be full")
	}
	time.Sleep(150 * time.Millisecond)
	r, _ := lb.Allow(ctx, key, rule, 1)
	if !r.Allowed || r.Delay != 0 {
		t.Fatalf("queue should have drained, no delay expected: %+v", r)
	}
}

func TestLeakyBucketCostAndValidation(t *testing.T) {
	rdb := testRedis(t)
	lb := NewLeakyBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := LeakyBucketRule{Capacity: 10, LeakPerSec: 0.001}

	if r, _ := lb.Allow(ctx, key, rule, 7); !r.Allowed || r.Remaining != 3 {
		t.Fatalf("cost 7: %+v", r)
	}
	if r, _ := lb.Allow(ctx, key, rule, 4); r.Allowed {
		t.Fatalf("cost 4 should not fit: %+v", r)
	}
	if _, err := lb.Allow(ctx, key, rule, 11); err != ErrCostTooHigh {
		t.Fatalf("want ErrCostTooHigh, got %v", err)
	}
	if _, err := lb.Allow(ctx, key, LeakyBucketRule{}, 1); err == nil {
		t.Fatal("want invalid rule error")
	}
	if ttl := rdb.PTTL(ctx, key).Val(); ttl <= 0 {
		t.Fatalf("ttl not set: %v", ttl)
	}
}

func TestLeakyBucketConcurrentAtomicity(t *testing.T) {
	rdb := testRedis(t)
	lb := NewLeakyBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := LeakyBucketRule{Capacity: 100, LeakPerSec: 0.001}

	const goroutines, perG = 50, 40
	var allowed, denied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perG; i++ {
				r, err := lb.Allow(context.Background(), key, rule, 1)
				if err != nil {
					t.Error(err)
					return
				}
				if r.Allowed {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if allowed.Load() != 100 || denied.Load() != 1900 {
		t.Fatalf("allowed=%d denied=%d, want exactly 100/1900", allowed.Load(), denied.Load())
	}
}
