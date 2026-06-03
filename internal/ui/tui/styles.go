package tui

import "github.com/charmbracelet/lipgloss"

// userEchoStyle renders the "> <input>" chip — bright text on dark grey
// with padding so the operator's turn is visually distinct from the agent's
// "● <reply>" turn even on non-color-aware terminals.
var userEchoStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("255")).
	Background(lipgloss.Color("237")).
	Padding(0, 1)

// formatUserEcho renders the submitted prompt as a transcript chip. Long
// real-world prompts must wrap inside the terminal instead of continuing as a
// single clipped row; keep the available width conservative so the chip's
// padding and the terminal edge do not fight each other on narrow screens.
func formatUserEcho(input string, terminalWidth int) string {
	wrap := terminalWidth - 4
	if wrap < 24 {
		wrap = 24
	}
	return userEchoStyle.Width(wrap).Render("> " + input)
}

// setupRequiredStyle is the red banner shown in-stream when the operator
// asks a question before /setup or /login has configured a model.
var setupRequiredStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("196")).Bold(true)

// assistantPrefixStyle styles the "●" bullet that anchors every agent
// response. Same brand sky-blue as the welcome banner so the cue feels
// of-a-piece with the rest of cloudy's chrome instead of an ad-hoc
// glyph dropped in front of the text.
var assistantPrefixStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("117")).Bold(true)

// approvalBannerStyle paints the first row of the RiskHigh approval
// banner. White on red, bold — impossible to scroll past while the
// agent goroutine waits on a y/n decision. The previous plain-text
// banner blended into the transcript during a fast tool sequence and
// the operator could miss that the agent had paused.
var approvalBannerStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("15")).
	Background(lipgloss.Color("196")).
	Bold(true).
	Padding(0, 1)

// approvalHintStyle is the muted second line ("press [y] / [n] / Esc").
// Lower contrast on purpose so the eye lands on the warning line first
// and treats the hint as supporting context.
var approvalHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("250"))
