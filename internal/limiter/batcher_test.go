package limiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestBatcher(t testing.TB, rdb *redis.Client) *Batcher {
	b := NewBatcher(rdb, 4, 64, time.Second)
	t.Cleanup(b.Close)
	return b
}

// Batching must not weaken atomicity: exactly Capacity of N concurrent requests may win.
func TestBatcherPreservesAtomicity(t *testing.T) {
	rdb := testRedis(t)
	tb := NewTokenBucket(newTestBatcher(t, rdb))
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	rule := TokenBucketRule{Capacity: 100, RefillPerSec: 0.0001}

	var allowed, denied atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
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
	wg.Wait()
	if allowed.Load() != 100 || denied.Load() != 1900 {
		t.Fatalf("allowed=%d denied=%d, want exactly 100/1900", allowed.Load(), denied.Load())
	}
}

// Each caller must get ITS OWN result back (no cross-talk between commands sharing a pipeline).
func TestBatcherRoutesResultsToCallers(t *testing.T) {
	rdb := testRedis(t)
	b := newTestBatcher(t, rdb)
	tb := NewTokenBucket(b)
	rule := TokenBucketRule{Capacity: 1000, RefillPerSec: 0.0001}

	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			key := fmt.Sprintf("rltest:{batch%d}:%d", g, time.Now().UnixNano())
			defer rdb.Del(context.Background(), key)
			cost := int64(g%10 + 1)
			r, err := tb.Allow(context.Background(), key, rule, cost)
			if err != nil || !r.Allowed || r.Remaining != 1000-cost { // remaining encodes this caller's cost
				t.Errorf("g=%d cost=%d: %+v err=%v", g, cost, r, err)
			}
		}(g)
	}
	wg.Wait()
}

func TestBatcherHonorsContextAndClose(t *testing.T) {
	rdb := testRedis(t)
	b := NewBatcher(rdb, 1, 8, time.Second)
	tb := NewTokenBucket(b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tb.Allow(ctx, uniqueKey(t), TokenBucketRule{Capacity: 1, RefillPerSec: 1}, 1); err == nil {
		t.Fatal("cancelled context must fail")
	}
	b.Close()
	if _, err := tb.Allow(context.Background(), uniqueKey(t), TokenBucketRule{Capacity: 1, RefillPerSec: 1}, 1); err == nil {
		t.Fatal("closed batcher must fail, not hang")
	}
}

func BenchmarkTokenBucketAllowBatched(b *testing.B) {
	rdb := testRedis(b)
	bt := NewBatcher(rdb, 4, 128, time.Second)
	defer bt.Close()
	tb := NewTokenBucket(bt)
	rule := TokenBucketRule{Capacity: 1_000_000, RefillPerSec: 1_000_000}
	var n atomic.Int64
	b.ReportAllocs()
	b.SetParallelism(16)
	b.RunParallel(func(pb *testing.PB) {
		key := fmt.Sprintf("rlbench:b:{%d}", n.Add(1)%64)
		for pb.Next() {
			if _, err := tb.Allow(context.Background(), key, rule, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// Regression: the request queue is buffered, so a send after Close used to succeed and then wait forever.
// Run many iterations because the old bug depended on select's random choice.
func TestBatcherNeverHangsAfterClose(t *testing.T) {
	rdb := testRedis(t)
	for i := 0; i < 200; i++ {
		b := NewBatcher(rdb, 1, 8, time.Second)
		b.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			cmd := b.EvalSha(context.Background(), "deadbeef", []string{"k"})
			if cmd.Err() == nil {
				t.Error("closed batcher must return an error")
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: EvalSha hung on a closed batcher", i)
		}
	}
}
