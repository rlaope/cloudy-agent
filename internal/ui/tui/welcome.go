package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

// WelcomeModel renders the cloudy banner shown above an empty stream.
// All inputs (firstRun, lastContext, noColor) are immutable after
// construction, so the rendered output is precomputed once and reused
// on every View() call — bubbletea redraws frequently and re-styling
// the ASCII block on every keystroke is wasted work.
type WelcomeModel struct {
	firstRun    bool
	lastContext string
	width       int
	noColor     bool
	cached      string
}

// NewWelcomeModel constructs a WelcomeModel and precomputes the rendered
// banner. firstRun=true appends the /setup, /help, ⏎ onboarding hints;
// firstRun=false appends a single dim line with the active context (when
// known) plus /setup and /help.
func NewWelcomeModel(firstRun bool, lastContext string) WelcomeModel {
	m := WelcomeModel{
		firstRun:    firstRun,
		lastContext: lastContext,
		width:       80,
		noColor:     os.Getenv("NO_COLOR") != "",
	}
	m.cached = m.renderFullBanner()
	return m
}

// SetWidth allows the parent to constrain the banner width. The banner
// content does not currently react to width (the ASCII block is fixed
// at 52 cols), so this is informational only.
func (m *WelcomeModel) SetWidth(w int) {
	m.width = w
}

// View returns the cached banner.
func (m WelcomeModel) View() string {
	return m.cached
}

const welcomeASCII = `
 ██████╗██╗      ██████╗ ██╗   ██╗██████╗ ██╗   ██╗
██╔════╝██║     ██╔═══██╗██║   ██║██╔══██╗╚██╗ ██╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║ ╚████╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║  ╚██╔╝
╚██████╗███████╗╚██████╔╝╚██████╔╝██████╔╝   ██║
 ╚═════╝╚══════╝ ╚═════╝  ╚═════╝ ╚═════╝    ╚═╝`

func (m WelcomeModel) renderFullBanner() string {
	// Sky-blue brand on the ASCII block reads well on both light and dark
	// terminals without competing with the body text below it.
	asciiStyle := lipgloss.NewStyle()
	taglineStyle := lipgloss.NewStyle()
	dim := lipgloss.NewStyle()
	if !m.noColor {
		asciiStyle = asciiStyle.Foreground(lipgloss.Color("117")).Bold(true)
		taglineStyle = taglineStyle.Foreground(lipgloss.Color("153"))
		dim = dim.Foreground(lipgloss.Color("8"))
	}

	tagline := fmt.Sprintf("cloudy %s — read-only multi-cluster SRE agent", buildinfo.Version)

	lines := []string{
		asciiStyle.Render(welcomeASCII),
		"",
		"  " + taglineStyle.Render(tagline),
	}

	if m.firstRun {
		hints := []string{
			"⚙  /setup    discover clusters & backends",
			"?  /help     keyboard shortcuts",
			"⏎           or just ask a question",
		}
		lines = append(lines, "")
		for _, hint := range hints {
			lines = append(lines, "  "+dim.Render(hint))
		}
	} else {
		segs := []string{}
		if m.lastContext != "" {
			segs = append(segs, "ctx="+m.lastContext)
		}
		segs = append(segs, "/setup", "/help")
		lines = append(lines, "  "+dim.Render(strings.Join(segs, "  ·  ")))
	}

	return strings.Join(lines, "\n")
}
