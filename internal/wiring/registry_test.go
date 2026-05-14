package wiring

import (
	"math/rand"
	"sync"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

// resetActive returns the active pointer to nil so each test starts clean.
// Tests in this file must call this at the top to be independent of order.
func resetActive() { active.Store(nil) }

func TestCurrentNilByDefault(t *testing.T) {
	resetActive()
	if got := Current(); got != nil {
		t.Fatalf("Current() = %v, want nil before any Replace()", got)
	}
}

func TestReplaceSwap(t *testing.T) {
	resetActive()
	t.Cleanup(resetActive)

	r1 := tools.New()
	r2 := tools.New()

	Replace(r1)
	if got := Current(); got != r1 {
		t.Fatalf("Current() = %p, want r1 %p", got, r1)
	}

	Replace(r2)
	if got := Current(); got != r2 {
		t.Fatalf("Current() = %p, want r2 %p", got, r2)
	}
}

func TestReplaceNil(t *testing.T) {
	resetActive()
	t.Cleanup(resetActive)

	Replace(tools.New())
	Replace(nil)
	if got := Current(); got != nil {
		t.Fatalf("Current() = %v, want nil after Replace(nil)", got)
	}
}

// TestReplaceConcurrent confirms that interleaved Replace and Current calls
// are race-free. Run with `go test -race` to exercise the atomic.Pointer
// guarantee.
func TestReplaceConcurrent(t *testing.T) {
	resetActive()
	t.Cleanup(resetActive)

	const n = 50
	registries := make([]*tools.Registry, n)
	for i := range registries {
		registries[i] = tools.New()
	}

	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// Deterministic but spread across the slice; rand here is only
			// to defeat any cache-locality artefacts.
			Replace(registries[rand.Intn(n)])
			_ = i
		}(i)
		go func() {
			defer wg.Done()
			_ = Current()
		}()
	}

	wg.Wait()
}
