package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

// WelcomeModel renders the cloudy banner shown above the empty stream when
// the user first enters the TUI. It is a stateless renderer; the parent
// Model decides when to display it.
type WelcomeModel struct {
	firstRun    bool
	lastContext string
	width       int
	noColor     bool
}

// NewWelcomeModel constructs a WelcomeModel.
//   firstRun=true draws the full ASCII banner.
//   firstRun=false draws the compact one-liner.
// lastContext is the kubeconfig context to display in the compact form.
func NewWelcomeModel(firstRun bool, lastContext string) WelcomeModel {
	noColor := os.Getenv("NO_COLOR") != ""
	return WelcomeModel{
		firstRun:    firstRun,
		lastContext: lastContext,
		width:       80,
		noColor:     noColor,
	}
}

// SetWidth allows the parent to constrain the banner width.
func (m *WelcomeModel) SetWidth(w int) {
	m.width = w
}

// View returns the rendered banner. Honors NO_COLOR / the noColor field.
func (m WelcomeModel) View() string {
	if m.firstRun {
		return m.renderFullBanner()
	}
	return m.renderCompactBanner()
}

func (m WelcomeModel) renderFullBanner() string {
	ascii := `
 ██████╗██╗      ██████╗ ██╗   ██╗██████╗ ██╗   ██╗
██╔════╝██║     ██╔═══██╗██║   ██║██╔══██╗╚██╗ ██╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║ ╚████╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║  ╚██╔╝
╚██████╗███████╗╚██████╔╝╚██████╔╝██████╔╝   ██║
 ╚═════╝╚══════╝ ╚═════╝  ╚═════╝ ╚═════╝    ╚═╝`

	tagline := fmt.Sprintf("cloudy %s — read-only multi-cluster SRE agent", buildinfo.Version)
	taglineStyle := lipgloss.NewStyle()
	if !m.noColor {
		taglineStyle = taglineStyle.Foreground(lipgloss.Color("6")) // cyan
	}

	// Command hints with dim glyphs
	hints := []string{
		"⚙  /setup    discover clusters & backends",
		"?  /help     keyboard shortcuts",
		"⏎           or just ask a question",
	}

	hintStyle := lipgloss.NewStyle()
	if !m.noColor {
		hintStyle = hintStyle.Foreground(lipgloss.Color("8")) // dim
	}

	lines := []string{
		ascii,
		"",
		"  " + taglineStyle.Render(tagline),
		"",
	}

	for _, hint := range hints {
		lines = append(lines, "  "+hintStyle.Render(hint))
	}

	return strings.Join(lines, "\n")
}

func (m WelcomeModel) renderCompactBanner() string {
	version := buildinfo.Version
	parts := []string{
		"cloudy " + version,
	}

	// Add context only if it's not empty
	if m.lastContext != "" {
		parts = append(parts, "ctx="+m.lastContext)
	}

	parts = append(parts, "/help", "/setup")

	content := strings.Join(parts, " · ")

	// Apply dim style if color is enabled
	if m.noColor {
		return content
	}

	style := lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // dim
	return style.Render(content)
}
