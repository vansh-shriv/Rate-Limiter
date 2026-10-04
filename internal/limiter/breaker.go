package limiter

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned without touching Redis while the breaker is open.
var ErrCircuitOpen = errors.New("limiter: circuit open (backend unavailable)")

// Breaker wraps a Limiter with a circuit breaker.
//
// Why: when Redis is down every decision would otherwise wait for the full Redis timeout (200 ms by default)
// before the fail-open/closed policy applies, adding that latency to every request and piling up goroutines.
// After `threshold` consecutive backend failures the breaker opens and rejects instantly for `cooldown`;
// then exactly one probe request is allowed through (half-open). A success closes it, a failure re-opens it.
//
// Only backend failures count. Validation errors (bad rule, cost too high) and the caller cancelling its own
// request say nothing about Redis health and are ignored.
type Breaker struct {
	next      Limiter
	threshold int
	cooldown  time.Duration
	onChange  func(open bool)
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time // zero value = closed
	probing   bool
}

var _ Limiter = (*Breaker)(nil)

// NewBreaker returns next unchanged semantics plus fail-fast. onChange (optional) is called outside the lock
// on every open/close transition.
func NewBreaker(next Limiter, threshold int, cooldown time.Duration, onChange func(open bool)) *Breaker {
	return &Breaker{next: next, threshold: threshold, cooldown: cooldown, onChange: onChange, now: time.Now}
}

func (b *Breaker) Allow(ctx context.Context, key string, rule Rule, cost int64) (Result, error) {
	if err := b.before(); err != nil {
		return Result{}, err
	}
	res, err := b.next.Allow(ctx, key, rule, cost)
	b.after(err)
	return res, err
}

func (b *Breaker) before() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return nil // closed
	}
	if b.now().Before(b.openUntil) {
		return ErrCircuitOpen
	}
	if b.probing {
		return ErrCircuitOpen // someone else is already probing
	}
	b.probing = true // half-open: this caller is the probe
	return nil
}

func (b *Breaker) after(err error) {
	failure := isBackendFailure(err)
	if err != nil && !failure {
		// Neutral outcome. If it was the probe, release the slot so another request can probe.
		b.mu.Lock()
		b.probing = false
		b.mu.Unlock()
		return
	}

	var changed, open bool
	b.mu.Lock()
	if failure {
		b.failures++
		if b.probing || b.failures >= b.threshold {
			wasClosed := b.openUntil.IsZero()
			b.openUntil = b.now().Add(b.cooldown)
			b.probing = false
			changed, open = wasClosed, true
		}
	} else {
		wasOpen := !b.openUntil.IsZero()
		b.failures, b.openUntil, b.probing = 0, time.Time{}, false
		changed, open = wasOpen, false
	}
	b.mu.Unlock()

	if changed && b.onChange != nil {
		b.onChange(open)
	}
}

func isBackendFailure(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrInvalidRule), errors.Is(err, ErrCostTooHigh), errors.Is(err, context.Canceled):
		return false
	default:
		return true
	}
}
