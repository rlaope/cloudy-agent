package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeProvider is a Provider whose Stream behavior is driven by a programmable
// hook. Tests use it to simulate transport-level failures and successes.
type fakeProvider struct {
	name string
	// streamFn handles the call. It receives the request and returns either a
	// chunk channel or an error.
	streamFn func(req Request) (<-chan Chunk, error)
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Stream(_ context.Context, req Request) (<-chan Chunk, error) {
	return f.streamFn(req)
}

func okChunks(text string) <-chan Chunk {
	ch := make(chan Chunk, 2)
	ch <- Chunk{DeltaText: text}
	ch <- Chunk{Done: true}
	close(ch)
	return ch
}

func TestFallback_PrimarySucceedsNoFallbackUsed(t *testing.T) {
	calls := map[string]int{}
	primary := &fakeProvider{name: "primary", streamFn: func(req Request) (<-chan Chunk, error) {
		calls[req.Model]++
		return okChunks("hello"), nil
	}}
	secondary := &fakeProvider{name: "secondary", streamFn: func(req Request) (<-chan Chunk, error) {
		calls[req.Model]++
		return okChunks("from-secondary"), nil
	}}
	fp := NewFallback(
		[]Provider{primary, secondary},
		[]string{"primary-model", "secondary-model"},
		FallbackOptions{},
	)
	ch, err := fp.Stream(context.Background(), Request{Model: "primary-model"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		out.WriteString(c.DeltaText)
	}
	if out.String() != "hello" {
		t.Errorf("got %q, want %q", out.String(), "hello")
	}
	if calls["secondary-model"] != 0 {
		t.Error("secondary should not have been called when primary succeeded")
	}
}

func TestFallback_PrimaryFailsSecondaryUsed(t *testing.T) {
	primary := &fakeProvider{name: "primary", streamFn: func(_ Request) (<-chan Chunk, error) {
		return nil, errors.New("primary down")
	}}
	secondary := &fakeProvider{name: "secondary", streamFn: func(req Request) (<-chan Chunk, error) {
		if req.Model != "secondary-model" {
			t.Errorf("secondary got wrong model: %q", req.Model)
		}
		return okChunks("rescued"), nil
	}}
	fp := NewFallback(
		[]Provider{primary, secondary},
		[]string{"primary-model", "secondary-model"},
		FallbackOptions{},
	)
	ch, err := fp.Stream(context.Background(), Request{Model: "primary-model"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		out.WriteString(c.DeltaText)
	}
	if out.String() != "rescued" {
		t.Errorf("got %q, want %q", out.String(), "rescued")
	}
}

func TestFallback_AllProvidersFail(t *testing.T) {
	primary := &fakeProvider{name: "primary", streamFn: func(_ Request) (<-chan Chunk, error) {
		return nil, errors.New("dead-1")
	}}
	secondary := &fakeProvider{name: "secondary", streamFn: func(_ Request) (<-chan Chunk, error) {
		return nil, errors.New("dead-2")
	}}
	fp := NewFallback(
		[]Provider{primary, secondary},
		[]string{"a", "b"},
		FallbackOptions{},
	)
	_, err := fp.Stream(context.Background(), Request{Model: "a"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "all 2 providers failed") {
		t.Errorf("error should report all-providers-failed: %v", err)
	}
}

func TestFallback_BreakerOpensAfterThreshold(t *testing.T) {
	calls := 0
	primary := &fakeProvider{name: "primary", streamFn: func(_ Request) (<-chan Chunk, error) {
		calls++
		return nil, errors.New("flap")
	}}
	secondary := &fakeProvider{name: "secondary", streamFn: func(_ Request) (<-chan Chunk, error) {
		return okChunks("ok"), nil
	}}
	fp := NewFallback(
		[]Provider{primary, secondary},
		[]string{"a", "b"},
		FallbackOptions{Threshold: 2, Window: time.Hour},
	)

	// Two failures should open the primary's breaker.
	for i := 0; i < 4; i++ {
		ch, err := fp.Stream(context.Background(), Request{Model: "a"})
		if err != nil {
			t.Fatalf("iter %d unexpected err: %v", i, err)
		}
		for range ch {
		}
	}
	// After 2 failures, the breaker should open and subsequent calls should
	// skip primary entirely — calls remains pinned at 2.
	if calls > 2 {
		t.Errorf("primary called %d times; breaker should have opened after 2", calls)
	}
}

func TestFallback_ContextCancelledDoesNotTripBreaker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &fakeProvider{name: "primary", streamFn: func(_ Request) (<-chan Chunk, error) {
		return nil, ctx.Err() // would normally trip breaker
	}}
	fp := NewFallback(
		[]Provider{primary},
		[]string{"a"},
		FallbackOptions{Threshold: 1, Window: time.Hour},
	)
	_, err := fp.Stream(ctx, Request{Model: "a"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	// Breaker stays closed.
	if fp.breakers[0].IsOpen() {
		t.Error("breaker should not open on context cancellation")
	}
}

func TestBreaker_OpenAndClose(t *testing.T) {
	b := newBreaker(2, 100*time.Millisecond)
	if b.IsOpen() {
		t.Error("breaker should start closed")
	}
	b.recordFailure()
	if b.IsOpen() {
		t.Error("one failure should not open breaker (threshold=2)")
	}
	b.recordFailure()
	if !b.IsOpen() {
		t.Error("two failures should open breaker")
	}
	// Wait past the window and verify auto-close.
	time.Sleep(120 * time.Millisecond)
	if b.IsOpen() {
		t.Error("breaker should close after window elapses")
	}
}

func TestWrapWithCompatFallback_NoEnvReturnsBare(t *testing.T) {
	t.Setenv(CompatFallbackBaseURLEnv, "")
	prim := &fakeProvider{name: "anthropic"}
	got := WrapWithCompatFallback(prim, "claude-x")
	if got != prim {
		t.Errorf("expected bare primary when env unset, got %T", got)
	}
}

func TestWrapWithCompatFallback_SamePrimaryReturnsBare(t *testing.T) {
	t.Setenv(CompatFallbackBaseURLEnv, "http://localhost:11434/v1")
	prim := &fakeProvider{name: "openai_compat"}
	got := WrapWithCompatFallback(prim, "llama3")
	if got != prim {
		t.Errorf("openai_compat primary should not be wrapped again, got %T", got)
	}
}
