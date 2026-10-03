package limiter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedis connects to RL_TEST_REDIS_ADDR (default localhost:6379); skips if unreachable.
func testRedis(tb testing.TB) *redis.Client {
	tb.Helper()
	addr := os.Getenv("RL_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 200})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		tb.Skipf("redis not available at %s: %v", addr, err)
	}
	tb.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func uniqueKey(tb testing.TB) string {
	k := fmt.Sprintf("rltest:{%s}:%d", tb.Name(), time.Now().UnixNano())
	return k
}

func TestTokenBucketBurstThenDeny(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := TokenBucketRule{Capacity: 5, RefillPerSec: 0.001} // effectively no refill

	for i := 0; i < 5; i++ {
		r, err := tb.Allow(context.Background(), key, rule, 1)
		if err != nil || !r.Allowed {
			t.Fatalf("req %d: allowed=%v err=%v", i, r.Allowed, err)
		}
		if want := int64(4 - i); r.Remaining != want {
			t.Fatalf("req %d: remaining=%d want %d", i, r.Remaining, want)
		}
	}
	r, err := tb.Allow(context.Background(), key, rule, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Allowed || r.RetryAfter <= 0 {
		t.Fatalf("expected denial with retry-after, got %+v", r)
	}
}

func TestTokenBucketRefill(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := TokenBucketRule{Capacity: 2, RefillPerSec: 10} // 1 token / 100ms

	ctx := context.Background()
	tb.Allow(ctx, key, rule, 2) // drain
	if r, _ := tb.Allow(ctx, key, rule, 1); r.Allowed {
		t.Fatal("should be empty")
	}
	time.Sleep(250 * time.Millisecond)
	if r, _ := tb.Allow(ctx, key, rule, 1); !r.Allowed {
		t.Fatal("should have refilled")
	}
}

func TestTokenBucketCostAndValidation(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	rule := TokenBucketRule{Capacity: 10, RefillPerSec: 0.001}

	if r, _ := tb.Allow(ctx, key, rule, 7); !r.Allowed || r.Remaining != 3 {
		t.Fatalf("cost 7: %+v", r)
	}
	if r, _ := tb.Allow(ctx, key, rule, 4); r.Allowed {
		t.Fatalf("cost 4 should be denied: %+v", r)
	}
	if _, err := tb.Allow(ctx, key, rule, 11); err != ErrCostTooHigh {
		t.Fatalf("want ErrCostTooHigh, got %v", err)
	}
	if _, err := tb.Allow(ctx, key, TokenBucketRule{}, 1); err == nil {
		t.Fatal("want invalid rule error")
	}
}

func TestTokenBucketSetsTTL(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	ctx := context.Background()
	tb.Allow(ctx, key, TokenBucketRule{Capacity: 10, RefillPerSec: 10}, 1)
	if ttl := rdb.PTTL(ctx, key).Val(); ttl <= 0 || ttl > 3*time.Second {
		t.Fatalf("unexpected ttl %v", ttl)
	}
}

// The core atomicity test: with no refill, exactly Capacity of N concurrent requests may win.
func TestTokenBucketConcurrentAtomicity(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(rdb)
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := TokenBucketRule{Capacity: 100, RefillPerSec: 0.0001}

	const goroutines, perG = 50, 40 // 2000 attempts
	var allowed, denied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perG; i++ {
				r, err := tb.Allow(context.Background(), key, rule, 1)
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

func BenchmarkTokenBucketAllow(b *testing.B) {
	rdb := testRedis(b)
	tb := NewTokenBucket(rdb)
	rule := TokenBucketRule{Capacity: 1_000_000, RefillPerSec: 1_000_000}
	var n atomic.Int64
	b.ReportAllocs()
	b.SetParallelism(16)
	b.RunParallel(func(pb *testing.PB) {
		key := fmt.Sprintf("rlbench:{%d}", n.Add(1)%64) // spread over 64 keys
		for pb.Next() {
			if _, err := tb.Allow(context.Background(), key, rule, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
}
