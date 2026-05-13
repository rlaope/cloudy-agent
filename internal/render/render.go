// Package render provides terminal-output helpers for the cloudy SRE agent:
// tables, sparklines, markdown, unified diffs, and streaming LLM output.
//
// # Color control
//
// Pass noColor=true (or set the NO_COLOR environment variable before process
// start) to obtain plain-text output with no ANSI escape sequences.  The
// --no-color CLI flag should read the env var and forward the result to
// [NewTheme].
//
// All renderers accept a [Theme] so colour decisions are made once at startup
// and threaded through without global state.
package render

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds the five lipgloss styles used across all renderers.
// When noColor is active every style is the zero value (no ANSI codes).
type Theme struct {
	// Mu is the muted / dim style used for secondary information.
	Mu lipgloss.Style
	// Hi is the highlight style for important values.
	Hi lipgloss.Style
	// Warn is the warning style (typically yellow/amber).
	Warn lipgloss.Style
	// Err is the error style (typically red).
	Err lipgloss.Style
	// Ok is the success style (typically green).
	Ok lipgloss.Style

	noColor bool
}

// NoColor reports whether this theme was built in no-colour mode.
func (t Theme) NoColor() bool { return t.noColor }

// NewTheme constructs a Theme.  Pass noColor=true or set the NO_COLOR
// environment variable to disable all ANSI colour output.
func NewTheme(noColor bool) Theme {
	// Honour the NO_COLOR spec (https://no-color.org/).
	if _, set := os.LookupEnv("NO_COLOR"); set {
		noColor = true
	}
	if noColor {
		return Theme{noColor: true}
	}
	return Theme{
		Mu:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Hi:      lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true),
		Warn:    lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Err:     lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		Ok:      lipgloss.NewStyle().Foreground(lipgloss.Color("82")),
		noColor: false,
	}
}
