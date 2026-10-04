package limiter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuleValidate(t *testing.T) {
	bad := []Rule{
		{},
		{Algorithm: "nope", Limit: 1, Window: time.Second},
		{Algorithm: TokenBucketAlgo, Limit: 0, Window: time.Second},
		{Algorithm: TokenBucketAlgo, Limit: 1, Window: 0},
		{Algorithm: TokenBucketAlgo, Limit: 1, Window: time.Second, Burst: -1},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, ErrInvalidRule) {
			t.Errorf("%+v: want ErrInvalidRule, got %v", r, err)
		}
	}
	if err := (Rule{Algorithm: LeakyBucketAlgo, Limit: 5, Window: time.Second}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// Every algorithm, reached through the Limiter interface, must enforce a limit of 5 (no meaningful refill within the test).
func TestRouterAllAlgorithms(t *testing.T) {
	rdb := testRedis(t)
	var l Limiter = NewRouter(rdb)
	ctx := context.Background()

	for _, algo := range []Algorithm{TokenBucketAlgo, SlidingWindowAlgo, LeakyBucketAlgo} {
		t.Run(string(algo), func(t *testing.T) {
			key := uniqueKey(t)
			t.Cleanup(func() { rdb.Del(ctx, key) })
			rule := Rule{Algorithm: algo, Limit: 5, Window: time.Hour}
			for i := 0; i < 5; i++ {
				r, err := l.Allow(ctx, key, rule, 1)
				if err != nil || !r.Allowed {
					t.Fatalf("req %d: %+v err=%v", i, r, err)
				}
			}
			if r, _ := l.Allow(ctx, key, rule, 1); r.Allowed {
				t.Fatal("6th request should be denied")
			}
		})
	}
}

func TestRouterBurstOverride(t *testing.T) {
	rdb := testRedis(t)
	l := NewRouter(rdb)
	ctx := context.Background()
	key := uniqueKey(t)
	t.Cleanup(func() { rdb.Del(ctx, key) })
	// 1/hour sustained, but a burst of 3 allowed.
	rule := Rule{Algorithm: TokenBucketAlgo, Limit: 1, Window: time.Hour, Burst: 3}
	for i := 0; i < 3; i++ {
		if r, _ := l.Allow(ctx, key, rule, 1); !r.Allowed {
			t.Fatalf("burst req %d denied", i)
		}
	}
	if r, _ := l.Allow(ctx, key, rule, 1); r.Allowed {
		t.Fatal("4th should be denied")
	}
}

func TestRouterRejectsInvalidRule(t *testing.T) {
	l := NewRouter(testRedis(t))
	if _, err := l.Allow(context.Background(), "k", Rule{Algorithm: "x"}, 1); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("got %v", err)
	}
}
