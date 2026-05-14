package tui

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

// TestFullView verifies that NewWelcomeModel(true, "") produces the full ASCII banner.
func TestFullView(t *testing.T) {
	m := NewWelcomeModel(true, "")
	view := m.View()

	if !strings.Contains(view, "cloudy") {
		t.Error("full banner should contain 'cloudy'")
	}
	if !strings.Contains(view, "/setup") {
		t.Error("full banner should contain '/setup'")
	}
	if !strings.Contains(view, "/help") {
		t.Error("full banner should contain '/help'")
	}
	if !strings.Contains(view, buildinfo.Version) {
		t.Error("full banner should contain buildinfo.Version")
	}
}

// TestCompactView verifies that NewWelcomeModel(false, "prod-eu") renders a single-line
// compact banner with the context included.
func TestCompactView(t *testing.T) {
	m := NewWelcomeModel(false, "prod-eu")
	view := m.View()

	if !strings.Contains(view, "ctx=prod-eu") {
		t.Error("compact banner should contain 'ctx=prod-eu'")
	}
	if !strings.Contains(view, "/setup") {
		t.Error("compact banner should contain '/setup'")
	}
}

// TestCompactNoContext verifies that NewWelcomeModel(false, "") does NOT include a "ctx="
// segment when lastContext is empty.
func TestCompactNoContext(t *testing.T) {
	m := NewWelcomeModel(false, "")
	view := m.View()

	if strings.Contains(view, "ctx=") {
		t.Error("compact banner with empty context should NOT contain 'ctx='")
	}
}

// TestNoColor verifies that when NO_COLOR env var is set, the output contains
// no ANSI escape codes.
func TestNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewWelcomeModel(true, "")
	view := m.View()

	if strings.Contains(view, "\x1b[") {
		t.Error("output should not contain ANSI escape codes when NO_COLOR is set")
	}
}
