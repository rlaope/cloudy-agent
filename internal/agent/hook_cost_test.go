package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"
)

func TestCostGuardHook_UnderTokenLimitDoesNotBlock(t *testing.T) {
	h := NewCostGuardHook(1000, 0)
	for i := 0; i < 5; i++ {
		if err := h.OnUsage(context.Background(), llm.Usage{InputTokens: 50, OutputTokens: 50}); err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
	}
	in, out, _ := h.Totals()
	if in+out != 500 {
		t.Errorf("totals = %d, want 500", in+out)
	}
}

func TestCostGuardHook_OverTokenLimitReturnsSentinel(t *testing.T) {
	h := NewCostGuardHook(100, 0)
	if err := h.OnUsage(context.Background(), llm.Usage{InputTokens: 40, OutputTokens: 40}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := h.OnUsage(context.Background(), llm.Usage{InputTokens: 40, OutputTokens: 40})
	if !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("got %v, want ErrTokenBudgetExceeded", err)
	}
}

func TestCostGuardHook_OverUSDLimitReturnsSentinel(t *testing.T) {
	h := NewCostGuardHook(0, 0.10)
	if err := h.OnUsage(context.Background(), llm.Usage{CostUSD: 0.05}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := h.OnUsage(context.Background(), llm.Usage{CostUSD: 0.10})
	if !errors.Is(err, ErrUSDBudgetExceeded) {
		t.Fatalf("got %v, want ErrUSDBudgetExceeded", err)
	}
}

func TestCostGuardHook_ZeroLimitsNeverBlock(t *testing.T) {
	h := NewCostGuardHook(0, 0)
	err := h.OnUsage(context.Background(), llm.Usage{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		CostUSD:      9999,
	})
	if err != nil {
		t.Fatalf("unexpected error with zero limits: %v", err)
	}
}

func TestTruncateMiddle_ShortInputUnchanged(t *testing.T) {
	in := "hello"
	if got := truncateMiddle(in, 100); got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestTruncateMiddle_PreservesHeadAndTail(t *testing.T) {
	head := "HEAD-PART-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	middle := "this should disappear lots of filler bytes here filling the buffer up"
	tail := "TAIL-PART-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	full := head + middle + tail
	out := truncateMiddle(full, 100)
	if len(out) > 100+40 { // marker length is bounded
		t.Errorf("truncated length %d exceeds max+marker", len(out))
	}
	if !strings.Contains(out, "HEAD-PART") {
		t.Errorf("head missing from output: %q", out)
	}
	if !strings.Contains(out, "TAIL-PART") {
		t.Errorf("tail missing from output: %q", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncation marker missing: %q", out)
	}
}
