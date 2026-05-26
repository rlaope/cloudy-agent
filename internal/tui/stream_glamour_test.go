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

	const raw = "Here is **bold** and a `code` span."
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
	updated, _ = s.Update(streamTokenMsg("Raw **markdown** stays."))
	s = updated
	s.drainPending()
	if !strings.Contains(s.content.String(), "**markdown**") {
		t.Errorf("noColor mode dropped raw markdown markers:\n%s", s.content.String())
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

	updated, _ = s.Update(streamTokenMsg("Some assistant prose for the resize check."))
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
