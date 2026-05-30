package tui

import (
	"strings"
	"testing"
)

// TestFooterCtxSegment verifies the footer renders a "ctx N%" gauge and
// clamps out-of-range input.
func TestFooterCtxSegment(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // deterministic plain rendering
	f := NewFooterModel("prod", "claude-x", "v1")
	f.SetCtxPct(42)
	if got := f.View(); !strings.Contains(got, "ctx 42%") {
		t.Errorf("footer missing ctx gauge: %q", got)
	}
	f.SetCtxPct(150)
	if got := f.View(); !strings.Contains(got, "ctx 100%") {
		t.Errorf("ctx not clamped to 100: %q", got)
	}
	f.SetCtxPct(-5)
	if got := f.View(); !strings.Contains(got, "ctx 0%") {
		t.Errorf("ctx not clamped to 0: %q", got)
	}
}

// TestContextPct exercises the Model gauge math, including the empty-history
// (pre-first-turn) zero case and the over-window clamp.
func TestContextPct(t *testing.T) {
	m := &Model{deps: Deps{Model: "claude-3-5-sonnet-20241022"}} // 200k window
	if got := m.contextPct(); got != 0 {
		t.Errorf("fresh session ctx = %d, want 0", got)
	}
	m.usage.LastInputTokens = 100_000
	if got := m.contextPct(); got != 50 {
		t.Errorf("ctx = %d, want 50", got)
	}
	m.usage.LastInputTokens = 999_999_999
	if got := m.contextPct(); got != 100 {
		t.Errorf("ctx = %d, want clamped 100", got)
	}
}
