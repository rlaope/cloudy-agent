package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestEscape_FromLoginChat_CancelsConversation drives /login → Esc and
// confirms the conversation ends with a cancellation note in the stream
// instead of leaving an orphan loginChat pointer.
func TestEscape_FromLoginChat_CancelsConversation(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "login"})
	if cmd != nil {
		cmd()
	}
	if m.loginChat == nil {
		t.Fatal("login should start a loginChat")
	}

	// Resolve the picker as cancelled to mimic the operator hitting Esc
	// while the picker is up. The picker branch (not the main Esc case)
	// catches Esc when an arrowPicker is active, so this is the path
	// that actually fires.
	next, outCmd := m.Update(arrowPickerResolveMsg{cancelled: true})
	m = next.(Model)
	if m.loginChat != nil {
		t.Error("loginChat should be cleared after picker cancel")
	}
	if m.arrowPicker != nil {
		t.Error("arrowPicker should be cleared after cancel")
	}
	if !strings.Contains(printedText(outCmd), "cancelled") {
		t.Errorf("scrollback should record the cancellation, got: %q",
			printedText(outCmd))
	}

	// Now confirm the plain-Esc path works once the operator is past
	// the picker and is on the key-prompt step (no picker on screen).
	m2 := NewModel(makeDeps())
	next2, _ := m2.Update(windowMsg())
	m2 = next2.(Model)
	cmd = m2.handlePaletteAction(paletteActionMsg{cmd: "login"})
	if cmd != nil {
		cmd()
	}
	// Simulate operator picking google → advance past picker.
	next2, _ = m2.Update(arrowPickerResolveMsg{key: "google"})
	m2 = next2.(Model)
	if m2.loginChat == nil {
		t.Fatal("loginChat should still be active on key-prompt step")
	}

	// Now plain Esc with no picker.
	next2, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 = next2.(Model)
	if m2.loginChat != nil {
		t.Error("loginChat should be cleared after plain Esc on key step")
	}
}

// TestEscape_ClearsPromptWhenNothingActive confirms Esc with no chat,
// no agent run, no overlays falls through to clearing the prompt so
// the keystroke is never a silent no-op.
func TestEscape_ClearsPromptWhenNothingActive(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Type some text into the prompt without submitting.
	for _, ch := range "draft text" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = next.(Model)
	}
	if m.prompt.Value() == "" {
		t.Fatal("prompt should contain typed draft text")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if m.prompt.Value() != "" {
		t.Errorf("Esc with nothing active should clear the prompt, got %q",
			m.prompt.Value())
	}
}

// TestEscape_AgentCancel_PreservesPrompt confirms Esc during an
// in-flight agent run cancels the run without wiping the prompt
// (so the operator can re-issue or edit their question).
func TestEscape_AgentCancel_PreservesPrompt(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Pretend an agent run is in flight.
	m.running = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.prompt.SetValue("a follow-up question")

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if !cancelled {
		t.Error("Esc during agent run should invoke the cancel func")
	}
	if m.running {
		t.Error("Esc during agent run should clear m.running")
	}
	if m.prompt.Value() != "a follow-up question" {
		t.Errorf("Esc during agent run should preserve the prompt for re-edit, got %q",
			m.prompt.Value())
	}
}
