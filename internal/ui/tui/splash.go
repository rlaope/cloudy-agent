package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// splashDuration is how long the boot splash (cloudy banner + animated dots)
// stays on screen before the main TUI takes over. Long enough to make the
// brand land, short enough that the operator never has to wait on it.
// The first KeyMsg also dismisses it early so eager typists never feel
// the brand getting in their way.
const splashDuration = 350 * time.Millisecond

// splashTickInterval drives the dots animation while the splash is visible.
const splashTickInterval = 90 * time.Millisecond

// splashTickMsg fires once per splashTickInterval until splashDuration elapses.
type splashTickMsg struct{}

// splashTickCmd returns a tea.Cmd that emits one splashTickMsg.
func splashTickCmd() tea.Cmd {
	return tea.Tick(splashTickInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
}

// splashState bundles the brand-banner gate. done flips once the
// splashDuration has elapsed; frame drives the dots animation.
type splashState struct {
	start time.Time
	done  bool
	frame int
}

// splashDotsStyle is the lighter sky-blue colour used by the splash
// trailer. Kept at package scope so the splash tick (120ms) does not
// rebuild the style on every frame.
var splashDotsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("153"))

// renderSplash returns the boot splash frame: the welcome banner with an
// animated "initialising…" trailer driven by splash.frame. Padded with a
// blank line so the body lands roughly mid-screen on a typical terminal.
func (m Model) renderSplash() string {
	banner := m.welcome.View()
	dots := strings.Repeat(".", (m.splash.frame%3)+1)
	trailer := "  " + splashDotsStyle.Render("initialising"+dots)
	return banner + "\n\n" + trailer
}
