package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPromptSelection_ShiftRightExtends pins the basic case: typing
// some text, pressing Home to move to the start, then Shift+Right a
// few times sets the anchor at 0 and leaves the cursor (= textarea's
// real cursor) one rune ahead per press. The selection range derives
// from |cursor - anchor|.
func TestPromptSelection_ShiftRightExtends(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p.ta.SetValue("hello world")
	p.ta.SetCursor(0) // start of line

	if p.selAnchor != -1 {
		t.Fatal("fresh prompt should not have an active selection")
	}

	for i := 0; i < 5; i++ {
		updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
		p = updated
	}

	if p.selAnchor != 0 {
		t.Errorf("anchor should be 0 after starting at start; got %d", p.selAnchor)
	}
	got := p.cursorRuneOffset()
	if got != 5 {
		t.Errorf("cursor should have moved to offset 5 after 5x Shift+Right; got %d", got)
	}
}

// TestPromptSelection_NonShiftKeyClears verifies the "any non-shift
// key exits selection mode" rule. Without this, the selection would
// stick across typing and subsequent Ctrl+Y would copy a stale range.
func TestPromptSelection_NonShiftKeyClears(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p.ta.SetValue("hello")
	p.ta.SetCursor(0)

	// Start a selection.
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
	p = updated
	if p.selAnchor < 0 {
		t.Fatal("expected anchor to be set after Shift+Right")
	}

	// Plain Right (no shift) must clear the anchor.
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	p = updated
	if p.selAnchor != -1 {
		t.Errorf("anchor should be cleared after a non-shift key; got %d", p.selAnchor)
	}
}

// TestPromptSelection_CopySelectionCmdReturnsCmd verifies Ctrl+Y
// produces a non-nil tea.Cmd when there is a selection and nil when
// there isn't. The actual OSC 52 byte emission is the Cmd's job and
// can't be observed from a pure unit test — but the *shape* of the
// integration (Cmd produced vs not) is the load-bearing contract.
func TestPromptSelection_CopySelectionCmdReturnsCmd(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p.ta.SetValue("hello")
	p.ta.SetCursor(0)

	// No selection yet — Ctrl+Y should produce nil.
	updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	p = updated
	if cmd != nil {
		t.Errorf("Ctrl+Y with no selection should produce nil cmd; got non-nil")
	}

	// Make a selection.
	for i := 0; i < 3; i++ {
		updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
		p = updated
	}

	updated, cmd = p.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	p = updated
	if cmd == nil {
		t.Errorf("Ctrl+Y with an active selection should produce an OSC 52 cmd; got nil")
	}
	// Anchor must clear after copy so the next Shift+arrow starts fresh.
	if p.selAnchor != -1 {
		t.Errorf("anchor should clear after Ctrl+Y copy; got %d", p.selAnchor)
	}
}

// TestPromptSelection_ViewHighlightsInPlace pins the rule that an
// active selection renders the chosen runes with reverse video IN
// PLACE (where they sit in the value), not as a separate `[sel: ...]`
// status block. Operator feedback after #79: the status block was
// noise — they wanted the GUI-text-editor model of highlighting the
// actual text.
func TestPromptSelection_ViewHighlightsInPlace(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p.ta.SetValue("abcdef")
	p.ta.SetCursor(0)
	if strings.Contains(p.View(), "[sel") {
		t.Errorf("fresh prompt should not show any selection block")
	}

	for i := 0; i < 3; i++ {
		updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyShiftRight})
		p = updated
	}

	out := p.View()
	if strings.Contains(out, "[sel") {
		t.Errorf("selection mode must NOT emit a `[sel ...]` status block; got:\n%s", out)
	}
	if strings.Contains(out, "ctrl+y") {
		t.Errorf("ctrl+y hint should be discoverable elsewhere (docs/help), not stamped into the prompt; got:\n%s", out)
	}
	// The full value must still appear so the operator sees the
	// content — the reverse-video ANSI wrapping doesn't strip text.
	if !strings.Contains(out, "abcdef") {
		t.Errorf("expected the typed value to be present in the rendered prompt; got:\n%s", out)
	}
}

// TestPromptSelection_BackwardsSelection verifies that selection
// works in both directions — Shift+Left from the end of a word
// should produce the same |cursor - anchor| count as Shift+Right
// from the start.
func TestPromptSelection_BackwardsSelection(t *testing.T) {
	p := newPromptModel(defaultKeys())
	p.ta.SetValue("hello")
	p.ta.CursorEnd() // cursor at offset 5

	for i := 0; i < 3; i++ {
		updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyShiftLeft})
		p = updated
	}

	if p.selAnchor != 5 {
		t.Errorf("anchor should be 5 (cursor end); got %d", p.selAnchor)
	}
	cur := p.cursorRuneOffset()
	if cur != 2 {
		t.Errorf("cursor should be at offset 2 after 3x Shift+Left; got %d", cur)
	}
}
