package llm

import (
	"sync"
	"time"
)

// breaker is a sliding-window failure counter. When the number of failures
// recorded within window crosses threshold, IsOpen returns true and callers
// should skip the resource it guards. Successful operations are not tracked —
// once the window passes, the breaker closes again automatically as old
// failures age out.
//
// The zero-value breaker is unusable; construct one via newBreaker.
type breaker struct {
	mu        sync.Mutex
	failures  []time.Time
	threshold int
	window    time.Duration
	now       func() time.Time // injected for tests
}

// newBreaker returns a breaker that opens after threshold failures in window.
func newBreaker(threshold int, window time.Duration) *breaker {
	return &breaker{
		threshold: threshold,
		window:    window,
		now:       time.Now,
	}
}

// recordFailure stamps a failure at the current clock and prunes any
// expired stamps so memory stays bounded.
func (b *breaker) recordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.failures = append(b.failures, now)
	b.pruneLocked(now)
}

// IsOpen reports whether failures within the window meet or exceed threshold.
func (b *breaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(b.now())
	return len(b.failures) >= b.threshold
}

// pruneLocked drops stamps older than now-window. Caller holds b.mu.
func (b *breaker) pruneLocked(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for ; i < len(b.failures); i++ {
		if b.failures[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		b.failures = b.failures[i:]
	}
}
