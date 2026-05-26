package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestStreamModel_TokenBatching_NoImmediateSetContent verifies that a
// streamTokenMsg appends to the pending buffer rather than flushing
// straight into the content. The batching layer is the fix for the
// "툭툭" stutter the user reported — every Anthropic SSE chunk used to
// trigger its own viewport reflow, and on long replies the per-token
// cost grew linearly because vp.SetContent rebuilds line state from a
// fresh copy of the whole accumulated buffer.
func TestStreamModel_TokenBatching_NoImmediateSetContent(t *testing.T) {
	s := newStreamModel(true)
	// Seed ready=true via WindowSizeMsg so the flush path mirrors the
	// real TUI; otherwise SetContent is skipped wholesale.
	s, _ = s.Update(windowMsg())

	s, cmd := s.Update(streamTokenMsg("hello"))
	if cmd == nil {
		t.Fatal("streamTokenMsg should arm a flush tick — got nil cmd")
	}
	if s.content.Len() != 0 {
		t.Errorf("content must NOT receive bytes until flush; got %q", s.content.String())
	}
	if s.pendingTokens.Len() == 0 {
		t.Error("streamTokenMsg should append to pendingTokens")
	}
	if !s.flushScheduled {
		t.Error("first streamTokenMsg should set flushScheduled=true")
	}
}

// TestStreamModel_TokenBatching_CoalescesBurst confirms that a tight
// burst of streamTokenMsg writes accumulates in the pending buffer and
// only one flush tick is armed for the whole batch. Without this the
// pump would queue one tick per token and the viewport would still
// reflow at SSE chunk rate.
func TestStreamModel_TokenBatching_CoalescesBurst(t *testing.T) {
	s := newStreamModel(true)
	s, _ = s.Update(windowMsg())

	tickArmed := 0
	for _, tok := range []string{"안", "녕", "하", "세", "요"} {
		var cmd tea.Cmd
		s, cmd = s.Update(streamTokenMsg(tok))
		if cmd != nil {
			tickArmed++
		}
	}
	if tickArmed != 1 {
		t.Errorf("burst of 5 tokens should arm exactly 1 flush tick (got %d)", tickArmed)
	}
	if got := s.pendingTokens.String(); got != "안녕하세요" {
		t.Errorf("all tokens should coalesce in pending; got %q", got)
	}
	if s.content.Len() != 0 {
		t.Error("content must stay empty until the flush tick fires")
	}
}

// TestStreamModel_FlushTick_DrainsPending checks the other end of the
// pipeline: when the flush tick arrives AND mdBuf has crossed a
// sentence boundary, the committed prefix lands in content and the
// viewport refreshes. Mid-sentence bursts do NOT commit under the
// sentence-batched streaming model — that path is covered by
// TestStreamModel_SentenceBatchedCommit (stream_glamour_test.go).
func TestStreamModel_FlushTick_DrainsPending(t *testing.T) {
	s := newStreamModel(true)
	s, _ = s.Update(windowMsg())

	s, _ = s.Update(streamTokenMsg("partial "))
	// Newline is an unambiguous sentence boundary, so the flush tick
	// commits as soon as it lands in mdBuf.
	s, _ = s.Update(streamTokenMsg("answer.\n"))

	s, _ = s.Update(streamFlushTickMsg{})

	if got := s.content.String(); !strings.Contains(got, "partial answer.") {
		t.Errorf("flush should drain the committed sentence into content; got %q", got)
	}
	if s.pendingTokens.Len() != 0 {
		t.Error("pendingTokens should be empty after flush")
	}
	if s.flushScheduled {
		t.Error("flushScheduled must reset so the next burst re-arms a tick")
	}
}

// TestStreamModel_WriteMsg_BypassesBatching is the assertion that
// distinguishes chrome writes from agent stream tokens: streamWriteMsg
// must land in content immediately so the parent's writeStream helper
// (and the user-echo line) are visible on the same Update tick they
// were issued on. Without this split, the test suite would have to
// pump a flush tick before reading any chrome output.
func TestStreamModel_WriteMsg_BypassesBatching(t *testing.T) {
	s := newStreamModel(true)
	s, _ = s.Update(windowMsg())

	// Stage a pending token, then issue a chrome write. The pending
	// token must drain first so the chrome line stays in submit order.
	s, _ = s.Update(streamTokenMsg("agent text "))
	s, _ = s.Update(streamWriteMsg("[error: thing]\n"))

	got := s.content.String()
	if !strings.Contains(got, "agent text ") {
		t.Errorf("chrome write should first drain pending agent text; got %q", got)
	}
	if !strings.Contains(got, "[error: thing]") {
		t.Errorf("chrome line must land in content synchronously; got %q", got)
	}
	if idx1, idx2 := strings.Index(got, "agent text "), strings.Index(got, "[error: thing]"); idx1 >= idx2 {
		t.Errorf("chrome line must follow the prior agent text, got order: %q", got)
	}
}

// TestStreamModel_AutoScroll_RespectsScrollback exercises the "stay
// where the operator put me" promise: when the operator scrolls up to
// read history, an incoming token must NOT yank the viewport back to
// the bottom. Pre-batching code called GotoBottom on every token.
func TestStreamModel_AutoScroll_RespectsScrollback(t *testing.T) {
	s := newStreamModel(true)
	s, _ = s.Update(windowMsg())
	// Pre-fill with enough content that the viewport scrolls.
	s, _ = s.Update(streamWriteMsg(strings.Repeat("filler line\n", 50)))
	// Operator scrolls up.
	s.vp.SetYOffset(0)
	wasAtBottom := s.vp.AtBottom()
	if wasAtBottom {
		t.Skip("viewport reports AtBottom even after SetYOffset(0); environment-specific")
	}

	// New token arrives + flush.
	s, _ = s.Update(streamTokenMsg("more text"))
	s, _ = s.Update(streamFlushTickMsg{})

	if s.vp.AtBottom() {
		t.Error("flush must not yank the viewport to the bottom when the operator was scrolled up")
	}
}
