package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestStreamModel_GlamourRendersAssistantMarkdown pins the new behaviour
// where assistant tokens accumulate in mdBuf and the rolling tail is
// re-rendered via glamour on every flush. The operator sees a polished
// rendering instead of the raw stream.
//
// Glamour's WithAutoStyle falls back to plain text when stdout isn't a
// TTY (always true under `go test`), so the bold markers DO survive the
// render in tests. We assert the structural invariants that hold in
// every style: the renderer was called (mdTailLen reflects the rendered
// length, which differs from the raw input because glamour wraps with
// margins) and the original content is preserved.
func TestStreamModel_GlamourRendersAssistantMarkdown(t *testing.T) {
	s := newStreamModel(false) // color mode → glamour active
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated
	if s.mdRenderer == nil {
		t.Fatal("WindowSizeMsg should have initialised the glamour renderer")
	}

	// Trailing newline triggers the sentence-batched commit (newline is
	// an unambiguous boundary). Without it the buffer would sit
	// uncommitted waiting for whitespace after the period — that's the
	// new sentence-by-sentence streaming model.
	const raw = "Here is **bold** and a `code` span.\n"
	updated, _ = s.Update(streamTokenMsg(raw))
	s = updated
	if !s.drainPending() {
		t.Fatal("expected drainPending to commit at least one byte")
	}

	out := s.content.String()
	if !strings.Contains(out, "bold") || !strings.Contains(out, "code") {
		t.Errorf("rendered output lost source content:\n%s", out)
	}
	// Renderer was called → tail length is set and differs from the raw
	// input length (glamour adds at least a leading and trailing margin).
	if s.mdTailLen == 0 {
		t.Errorf("mdTailLen should be set after drainPending (renderer was not invoked)")
	}
	if s.mdTailLen == len(raw) {
		t.Errorf("mdTailLen == len(raw) means glamour was a no-op; expected wrapped output")
	}
}

// TestStreamModel_GlamourFinalizesOnToolBegin verifies that a tool
// boundary commits the rendered tail so the next assistant chunk
// doesn't overwrite it. Without finalize, two assistant runs across a
// tool call would race on the same tail offset and the first run's
// output would vanish on the second flush.
func TestStreamModel_GlamourFinalizesOnToolBegin(t *testing.T) {
	s := newStreamModel(false)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated

	updated, _ = s.Update(streamTokenMsg("Before tool: **alpha**\n"))
	s = updated
	s.drainPending()
	beforeLen := s.content.Len()

	// Tool boundary → finalize. After this, the rendered "alpha" block
	// must be permanent — a second assistant chunk should append, not
	// overwrite.
	updated, _ = s.Update(streamToolBeginMsg{name: "k8s.list_pods", args: "{}"})
	s = updated
	updated, _ = s.Update(streamTokenMsg("After tool: **beta**\n"))
	s = updated
	s.drainPending()

	out := s.content.String()
	// Both contents should be present in the final buffer.
	if !strings.Contains(out, "alpha") {
		t.Errorf("pre-tool 'alpha' was clobbered after tool boundary:\n%s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("post-tool 'beta' missing from output:\n%s", out)
	}
	if s.content.Len() <= beforeLen {
		t.Errorf("expected content to grow past %d bytes after second flush, got %d", beforeLen, s.content.Len())
	}
}

// TestStreamModel_NoColor_StaysRaw covers the inverse: tests using
// noColor mode (the rest of the suite) must continue to see raw markdown
// in the content buffer because glamour is intentionally bypassed.
func TestStreamModel_NoColor_StaysRaw(t *testing.T) {
	s := newStreamModel(true)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated
	// Trailing newline so sentence-batched drainPending commits.
	updated, _ = s.Update(streamTokenMsg("Raw **markdown** stays.\n"))
	s = updated
	s.drainPending()
	if !strings.Contains(s.content.String(), "**markdown**") {
		t.Errorf("noColor mode dropped raw markdown markers:\n%s", s.content.String())
	}
}

// TestStreamModel_SentenceBatchedCommit pins the new streaming model:
// drainPending only advances the visible content when a sentence
// terminator + whitespace boundary lands in mdBuf past mdCommitted.
// Tokens that land mid-sentence accumulate silently — they don't paint
// half-words to the viewport every 16 ms like the old re-render-every-
// flush behaviour did.
func TestStreamModel_SentenceBatchedCommit(t *testing.T) {
	s := newStreamModel(false)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated

	// Token 1: mid-sentence, no terminator → must NOT commit.
	updated, _ = s.Update(streamTokenMsg("Mid sent"))
	s = updated
	if s.drainPending() {
		t.Errorf("drainPending should return false for a mid-sentence buffer")
	}
	if s.mdCommitted != 0 {
		t.Errorf("mdCommitted should stay 0 before the first sentence ends; got %d", s.mdCommitted)
	}

	// Token 2: completes the sentence + adds a space → COMMIT.
	updated, _ = s.Update(streamTokenMsg("ence. "))
	s = updated
	if !s.drainPending() {
		t.Fatal("drainPending should commit once `.` + ` ` is in the buffer")
	}
	if s.mdCommitted == 0 {
		t.Errorf("mdCommitted should advance past the committed sentence")
	}
	committedAfterFirst := s.mdCommitted

	// Token 3: starts the next sentence — uncommitted until its
	// terminator arrives.
	updated, _ = s.Update(streamTokenMsg("Next sentence"))
	s = updated
	if s.drainPending() {
		t.Errorf("drainPending should not advance on a partial second sentence")
	}
	if s.mdCommitted != committedAfterFirst {
		t.Errorf("mdCommitted should stay at %d while the second sentence is partial; got %d", committedAfterFirst, s.mdCommitted)
	}

	// Token 4: terminator + space → commit.
	updated, _ = s.Update(streamTokenMsg(". "))
	s = updated
	if !s.drainPending() {
		t.Fatal("drainPending should commit once the second sentence terminates")
	}
	if s.mdCommitted <= committedAfterFirst {
		t.Errorf("mdCommitted should have advanced past the second sentence; was %d, now %d", committedAfterFirst, s.mdCommitted)
	}
}

// TestStreamModel_ClauseBoundaryCommits pins the expanded boundary
// set: a clause separator (`,`, `;`, `:`) followed by whitespace
// commits the prefix just like a sentence terminator does. The narrow
// sentence-only rule used to leave long enumerated sentences sitting
// invisible until the final period; clause-level commit makes the
// streamed output read at the same pace the operator scans it.
func TestStreamModel_ClauseBoundaryCommits(t *testing.T) {
	s := newStreamModel(false)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated

	// Comma + space mid-sentence should already commit the prefix —
	// without this rule a 30-word enumerated sentence would sit
	// invisible until its final period.
	updated, _ = s.Update(streamTokenMsg("First clause, "))
	s = updated
	if !s.drainPending() {
		t.Fatal("comma + space should commit just like a sentence terminator")
	}
	committedAfterComma := s.mdCommitted
	if committedAfterComma == 0 {
		t.Errorf("mdCommitted should advance after `, `; got 0")
	}

	// Semicolon also counts.
	updated, _ = s.Update(streamTokenMsg("second; "))
	s = updated
	if !s.drainPending() {
		t.Fatal("semicolon + space should commit")
	}
	if s.mdCommitted <= committedAfterComma {
		t.Errorf("mdCommitted should advance past `; `; was %d still %d", committedAfterComma, s.mdCommitted)
	}
}

// TestStreamModel_FinalizeForcesUncommittedTail pins the rule that a
// tool / chrome boundary force-commits whatever's left in mdBuf, even
// if the message ended mid-sentence. Without this, an LLM that omits
// the final terminator (or that gets cut off) would silently lose its
// trailing clause.
func TestStreamModel_FinalizeForcesUncommittedTail(t *testing.T) {
	s := newStreamModel(false)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated

	updated, _ = s.Update(streamTokenMsg("Message ends mid-clause and is cut off"))
	s = updated
	s.drainPending() // no-op — no boundary

	if s.mdCommitted != 0 {
		t.Fatalf("precondition: nothing should be committed yet; got %d", s.mdCommitted)
	}
	beforeContent := s.content.String()

	s.finalizeAssistantBlock()

	if s.content.String() == beforeContent {
		t.Errorf("finalize should have force-committed the uncommitted tail; content unchanged")
	}
	if s.mdBuf.Len() != 0 || s.mdCommitted != 0 {
		t.Errorf("finalize should reset the buffer and committed offset; got mdBuf=%d mdCommitted=%d", s.mdBuf.Len(), s.mdCommitted)
	}
}

// TestStreamModel_GlamourRerendersOnResize pins the new behaviour where a
// WindowSizeMsg arriving AFTER the assistant tokens have stopped
// streaming re-renders the in-flight mdBuf at the new width — otherwise
// the visible wrap stayed at the old viewport's width until the next
// token or chrome event triggered drainPending. Without this the
// operator who resizes their terminal mid-conversation sees a
// stale-looking last message.
func TestStreamModel_GlamourRerendersOnResize(t *testing.T) {
	s := newStreamModel(false)
	updated, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = updated

	// Trailing newline so sentence-batched drainPending advances
	// mdCommitted and produces a non-zero mdTailLen on the first flush.
	updated, _ = s.Update(streamTokenMsg("Some assistant prose for the resize check.\n"))
	s = updated
	s.drainPending()
	originalTail := s.mdTailLen
	if originalTail == 0 {
		t.Fatal("expected mdTailLen to be set after the first flush")
	}

	// Tokens have stopped (no further streamTokenMsg). Now resize.
	updated, _ = s.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	s = updated

	if s.mdTailLen == 0 {
		t.Fatal("resize while mdBuf is non-empty should leave a fresh rendered tail")
	}
	if s.mdTailLen == originalTail {
		t.Errorf("expected tail length to change at the new wrap width (was %d, still %d)", originalTail, s.mdTailLen)
	}
}
