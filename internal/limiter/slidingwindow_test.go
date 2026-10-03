package limiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlidingWindowLimitThenDeny(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := SlidingWindowRule{Limit: 3, Window: time.Minute}

	for i := 0; i < 3; i++ {
		r, err := sw.Allow(ctx, key, rule, 1)
		if err != nil || !r.Allowed || r.Remaining != int64(2-i) {
			t.Fatalf("req %d: %+v err=%v", i, r, err)
		}
	}
	r, _ := sw.Allow(ctx, key, rule, 1)
	if r.Allowed || r.Remaining != 0 || r.RetryAfter <= 0 || r.RetryAfter > time.Minute {
		t.Fatalf("expected denial, got %+v", r)
	}
}

func TestSlidingWindowSlides(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := SlidingWindowRule{Limit: 2, Window: 300 * time.Millisecond}

	sw.Allow(ctx, key, rule, 2)
	if r, _ := sw.Allow(ctx, key, rule, 1); r.Allowed {
		t.Fatal("window full, should deny")
	}
	time.Sleep(350 * time.Millisecond)
	if r, _ := sw.Allow(ctx, key, rule, 1); !r.Allowed {
		t.Fatal("old entries should have slid out")
	}
}

// A fixed-window counter would let 2x the limit through around a boundary.
// The sliding log must not: after using the full limit, a request half a window later is still denied,
// and RetryAfter points at when the oldest entry leaves.
func TestSlidingWindowNoBoundaryBurst(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := SlidingWindowRule{Limit: 5, Window: 400 * time.Millisecond}

	sw.Allow(ctx, key, rule, 5)
	time.Sleep(200 * time.Millisecond)
	r, _ := sw.Allow(ctx, key, rule, 5)
	if r.Allowed {
		t.Fatal("boundary burst allowed")
	}
	if r.RetryAfter < 100*time.Millisecond || r.RetryAfter > 250*time.Millisecond {
		t.Fatalf("retry-after %v not ~200ms", r.RetryAfter)
	}
}

func TestSlidingWindowCostAndValidation(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := SlidingWindowRule{Limit: 10, Window: time.Minute}

	if r, _ := sw.Allow(ctx, key, rule, 7); !r.Allowed || r.Remaining != 3 {
		t.Fatalf("cost 7: %+v", r)
	}
	r, _ := sw.Allow(ctx, key, rule, 4)
	if r.Allowed || r.Remaining != 3 {
		t.Fatalf("cost 4 should be denied without consuming: %+v", r)
	}
	if _, err := sw.Allow(ctx, key, rule, 11); err != ErrCostTooHigh {
		t.Fatalf("want ErrCostTooHigh, got %v", err)
	}
	if _, err := sw.Allow(ctx, key, SlidingWindowRule{}, 1); err == nil {
		t.Fatal("want invalid rule error")
	}
}

func TestSlidingWindowSetsTTL(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	sw.Allow(ctx, key, SlidingWindowRule{Limit: 10, Window: 2 * time.Second}, 1)
	if ttl := rdb.PTTL(ctx, key).Val(); ttl <= 0 || ttl > 2*time.Second {
		t.Fatalf("unexpected ttl %v", ttl)
	}
}

// Exactly Limit of N concurrent requests may win; unique members guarantee no ZADD collisions.
func TestSlidingWindowConcurrentAtomicity(t *testing.T) {
	rdb := testRedis(t)
	sw := NewSlidingWindow(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := SlidingWindowRule{Limit: 100, Window: time.Minute}

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
				r, err := sw.Allow(context.Background(), key, rule, 1)
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
	if n := rdb.ZCard(context.Background(), key).Val(); n != 100 {
		t.Fatalf("zset has %d members, want 100", n)
	}
}

func BenchmarkSlidingWindowAllow(b *testing.B) {
	rdb := testRedis(b)
	sw := NewSlidingWindow(rdb)
	rule := SlidingWindowRule{Limit: 100, Window: time.Second}
	var n atomic.Int64
	b.ReportAllocs()
	b.SetParallelism(16)
	b.RunParallel(func(pb *testing.PB) {
		key := fmt.Sprintf("rlbench:sw:{%d}", n.Add(1)%64)
		for pb.Next() {
			if _, err := sw.Allow(context.Background(), key, rule, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
}
