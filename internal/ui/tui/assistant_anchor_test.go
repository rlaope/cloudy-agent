package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAssistantAnchor_ResetOnCtrlL guards against a regression where
// the assistantTurnStarted flag stayed `true` after Ctrl+L (clear),
// causing the next agent response to materialise with no leading "●"
// bullet — defeating the visual anchor the streaming-smoothness PR
// introduced.
//
// Sequence: simulate a turn that has already started (turn flag = true),
// fire the Ctrl+L key handler, then assert the flag is back to false.
func TestAssistantAnchor_ResetOnCtrlL(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.assistantTurnStarted = true

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(Model)

	if m.assistantTurnStarted {
		t.Error("Ctrl+L must reset assistantTurnStarted so the next response gets a fresh ● bullet")
	}
}

// TestAssistantAnchor_ResetOnSlashClear is the palette-action twin of
// the Ctrl+L test — same invariant, different code path.
func TestAssistantAnchor_ResetOnSlashClear(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.assistantTurnStarted = true

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "clear"})

	if m.assistantTurnStarted {
		t.Error("/clear must reset assistantTurnStarted so the next response gets a fresh ● bullet")
	}
}
