package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// fakeSummaryProvider is a minimal llm.Provider that returns a canned reply
// (or an error) so the compaction summarizer can be exercised hermetically.
type fakeSummaryProvider struct {
	reply string
	err   error
}

func (f *fakeSummaryProvider) Name() string { return "fake" }

func (f *fakeSummaryProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{DeltaText: f.reply}
	ch <- llm.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func longHistory(n int) []llm.Message {
	out := make([]llm.Message, n)
	for i := range out {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		out[i] = llm.Message{Role: role, Content: "msg"}
	}
	return out
}

func TestCompactHistory_Hybrid(t *testing.T) {
	ref := &providerRef{provider: &fakeSummaryProvider{reply: "DENSE SUMMARY"}, model: "claude-x"}
	state := &convoState{history: longHistory(20)} // sess nil → no disk write

	compact := makeCompactHistory(ref, state)
	summary, err := compact(context.Background())
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if summary != "DENSE SUMMARY" {
		t.Errorf("summary = %q", summary)
	}

	// Hybrid: exactly one summary head + the kept recent window.
	if len(state.history) != 1+compactKeepMessages {
		t.Fatalf("history len = %d, want %d", len(state.history), 1+compactKeepMessages)
	}
	head := state.history[0]
	if head.Role != llm.RoleUser {
		t.Errorf("summary head role = %q, want user (system role would be hoisted by providers)", head.Role)
	}
	if !strings.HasPrefix(head.Content, summaryPrefix) {
		t.Errorf("summary head missing prefix: %q", head.Content)
	}
}

func TestCompactHistory_Idempotent(t *testing.T) {
	ref := &providerRef{provider: &fakeSummaryProvider{reply: "S2"}, model: "claude-x"}
	// Head is already a prior summary; it sits in the older slice and folds in.
	hist := append([]llm.Message{{Role: llm.RoleUser, Content: summaryPrefix + "S1"}}, longHistory(15)...)
	state := &convoState{history: hist}

	if _, err := makeCompactHistory(ref, state)(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}
	// Still exactly one summary head (the old one was folded, not stacked).
	heads := 0
	for _, m := range state.history {
		if strings.HasPrefix(m.Content, summaryPrefix) {
			heads++
		}
	}
	if heads != 1 {
		t.Errorf("found %d summary messages, want exactly 1 (fold, not stack)", heads)
	}
}

func TestCompactHistory_FailureLeavesHistoryIntact(t *testing.T) {
	ref := &providerRef{provider: &fakeSummaryProvider{err: errors.New("boom")}, model: "claude-x"}
	orig := longHistory(20)
	state := &convoState{history: orig}

	if _, err := makeCompactHistory(ref, state)(context.Background()); err == nil {
		t.Fatal("expected error from failing summarizer")
	}
	if len(state.history) != 20 {
		t.Errorf("history mutated on failure: len = %d, want 20", len(state.history))
	}
}

func TestCompactHistory_ShortHistoryNoOp(t *testing.T) {
	ref := &providerRef{provider: &fakeSummaryProvider{reply: "x"}, model: "claude-x"}
	state := &convoState{history: longHistory(compactKeepMessages)}
	if _, err := makeCompactHistory(ref, state)(context.Background()); err == nil {
		t.Fatal("expected a 'nothing to compact' error for short history")
	}
}

func TestResetHistory_ClearsHistory(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	state := &convoState{history: longHistory(5)}
	id, err := makeResetHistory(state)()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if id == "" {
		t.Error("expected a new session id")
	}
	if len(state.history) != 0 {
		t.Errorf("history not cleared: %d", len(state.history))
	}
	if state.sess == nil {
		t.Error("expected a fresh session on state")
	}
}

func TestSeedHistory_LoadsHistory(t *testing.T) {
	state := &convoState{}
	seed := longHistory(3)
	makeSeedHistory(state)(seed)
	if len(state.history) != 3 {
		t.Errorf("seed not applied: %d", len(state.history))
	}
}
