package limiter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedLimiter struct {
	err   atomic.Value // error or nil
	calls atomic.Int64
}

func (s *scriptedLimiter) set(err error) { s.err.Store(struct{ error }{err}) }
func (s *scriptedLimiter) Allow(context.Context, string, Rule, int64) (Result, error) {
	s.calls.Add(1)
	if v, ok := s.err.Load().(struct{ error }); ok && v.error != nil {
		return Result{}, v.error
	}
	return Result{Allowed: true}, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newTestBreaker(next Limiter, threshold int, onChange func(bool)) (*Breaker, *clock) {
	b := NewBreaker(next, threshold, 2*time.Second, onChange)
	c := &clock{t: time.Unix(1000, 0)}
	b.now = c.now
	return b, c
}

var boom = errors.New("redis down")

func allow(b *Breaker) error {
	_, err := b.Allow(context.Background(), "k", Rule{}, 1)
	return err
}

func TestBreakerOpensAfterConsecutiveFailuresAndFailsFast(t *testing.T) {
	s := &scriptedLimiter{}
	s.set(boom)
	var transitions []bool
	b, _ := newTestBreaker(s, 3, func(open bool) { transitions = append(transitions, open) })

	for i := 0; i < 3; i++ {
		if err := allow(b); !errors.Is(err, boom) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	before := s.calls.Load()
	for i := 0; i < 10; i++ {
		if err := allow(b); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("open breaker must fail fast, got %v", err)
		}
	}
	if s.calls.Load() != before {
		t.Fatal("open breaker must not call the backend")
	}
	if len(transitions) != 1 || !transitions[0] {
		t.Fatalf("transitions: %v", transitions)
	}
}

func TestBreakerSuccessResetsConsecutiveCount(t *testing.T) {
	s := &scriptedLimiter{}
	b, _ := newTestBreaker(s, 3, nil)
	for i := 0; i < 5; i++ { // fail, fail, succeed: never reaches 3 in a row
		s.set(boom)
		allow(b)
		allow(b)
		s.set(nil)
		if err := allow(b); err != nil {
			t.Fatalf("round %d: breaker opened on non-consecutive failures: %v", i, err)
		}
	}
}

func TestBreakerHalfOpenProbeClosesOnSuccess(t *testing.T) {
	s := &scriptedLimiter{}
	s.set(boom)
	var transitions []bool
	b, c := newTestBreaker(s, 2, func(open bool) { transitions = append(transitions, open) })
	allow(b)
	allow(b) // open
	c.add(3 * time.Second)

	s.set(nil)
	if err := allow(b); err != nil {
		t.Fatalf("probe should reach the backend and succeed: %v", err)
	}
	if err := allow(b); err != nil {
		t.Fatalf("breaker should be closed after a successful probe: %v", err)
	}
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("transitions: %v", transitions)
	}
}

func TestBreakerFailedProbeReopens(t *testing.T) {
	s := &scriptedLimiter{}
	s.set(boom)
	b, c := newTestBreaker(s, 2, nil)
	allow(b)
	allow(b)
	c.add(3 * time.Second)
	if err := allow(b); !errors.Is(err, boom) { // the probe, fails
		t.Fatalf("probe: %v", err)
	}
	if err := allow(b); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("failed probe must re-open: %v", err)
	}
}

func TestBreakerOnlyOneProbeAtATime(t *testing.T) {
	probeStarted, release := make(chan struct{}), make(chan struct{})
	slow := limiterFunc(func(context.Context, string, Rule, int64) (Result, error) {
		close(probeStarted)
		<-release
		return Result{Allowed: true}, nil
	})
	b, c := newTestBreaker(slow, 1, nil)
	b.openUntil = c.now().Add(-time.Second) // cooldown already over, still "open" until a probe succeeds

	done := make(chan error, 1)
	go func() { done <- allow(b) }()
	<-probeStarted
	if err := allow(b); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second caller during probe must be rejected, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBreakerIgnoresCallerAndValidationErrors(t *testing.T) {
	s := &scriptedLimiter{}
	b, _ := newTestBreaker(s, 2, nil)
	for _, e := range []error{context.Canceled, ErrCostTooHigh, ErrInvalidRule} {
		s.set(e)
		for i := 0; i < 5; i++ {
			if err := allow(b); errors.Is(err, ErrCircuitOpen) {
				t.Fatalf("%v must not trip the breaker", e)
			}
		}
	}
}

type limiterFunc func(context.Context, string, Rule, int64) (Result, error)

func (f limiterFunc) Allow(ctx context.Context, k string, r Rule, c int64) (Result, error) {
	return f(ctx, k, r, c)
}
