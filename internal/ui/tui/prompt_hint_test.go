package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPromptModel_PlaceholderEmptyHistory verifies the baseline: with
// no prior submissions, the placeholder is the plain "ask cloudy…"
// invitation. The "↑ for history" affordance should not appear yet —
// suggesting history before any exists would just be a lie.
func TestPromptModel_PlaceholderEmptyHistory(t *testing.T) {
	p := newPromptModel(defaultKeys())
	if p.ta.Placeholder != "ask cloudy…" {
		t.Errorf("first-launch placeholder should be %q; got %q", "ask cloudy…", p.ta.Placeholder)
	}
}

// TestPromptModel_PlaceholderAfterSubmit confirms that once at least
// one prompt has been submitted (and therefore landed in history),
// syncHeight upgrades the placeholder to surface the up-arrow recall
// shortcut. Without this, users who never opened /help had no way to
// discover that history navigation exists.
func TestPromptModel_PlaceholderAfterSubmit(t *testing.T) {
	p := newPromptModel(defaultKeys())

	// Type a value, then press Enter to push it into history.
	p.ta.SetValue("first question")
	pNext, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on a non-empty non-slash prompt should produce a submit cmd")
	}
	p = pNext
	// The submit Cmd returns a submitMsg; it is enough that history
	// gained an entry. Drive a no-op key so syncHeight refreshes.
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	if !strings.Contains(p.ta.Placeholder, "↑ for history") {
		t.Errorf("after a submit, placeholder should advertise history recall; got %q", p.ta.Placeholder)
	}
}
