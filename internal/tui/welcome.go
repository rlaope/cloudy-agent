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
//
//	firstRun=true draws the full ASCII banner.
//	firstRun=false draws the compact one-liner.
//
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
// The cloudy ASCII art is drawn on every launch (not just the first run);
// firstRun only controls whether the /setup discovery hints are appended
// below the banner.
func (m WelcomeModel) View() string {
	return m.renderFullBanner()
}

func (m WelcomeModel) renderFullBanner() string {
	ascii := `
 ██████╗██╗      ██████╗ ██╗   ██╗██████╗ ██╗   ██╗
██╔════╝██║     ██╔═══██╗██║   ██║██╔══██╗╚██╗ ██╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║ ╚████╔╝
██║     ██║     ██║   ██║██║   ██║██║  ██║  ╚██╔╝
╚██████╗███████╗╚██████╔╝╚██████╔╝██████╔╝   ██║
 ╚═════╝╚══════╝ ╚═════╝  ╚═════╝ ╚═════╝    ╚═╝`

	// Sky-blue brand colour for the banner. 117 (SkyBlue1) reads well on
	// both light and dark terminals without competing with the body text.
	asciiStyle := lipgloss.NewStyle()
	if !m.noColor {
		asciiStyle = asciiStyle.Foreground(lipgloss.Color("117")).Bold(true)
	}

	tagline := fmt.Sprintf("cloudy %s — read-only multi-cluster SRE agent", buildinfo.Version)
	taglineStyle := lipgloss.NewStyle()
	if !m.noColor {
		taglineStyle = taglineStyle.Foreground(lipgloss.Color("153")) // lighter sky blue
	}

	lines := []string{
		asciiStyle.Render(ascii),
		"",
		"  " + taglineStyle.Render(tagline),
	}

	// First-launch hints are appended only when the user has no config yet,
	// so a returning operator sees the brand banner without the onboarding
	// noise. The status footer (cloudy …| state … | model …) carries the
	// at-a-glance context info every launch.
	if m.firstRun {
		hintStyle := lipgloss.NewStyle()
		if !m.noColor {
			hintStyle = hintStyle.Foreground(lipgloss.Color("8")) // dim
		}
		hints := []string{
			"⚙  /setup    discover clusters & backends",
			"?  /help     keyboard shortcuts",
			"⏎           or just ask a question",
		}
		lines = append(lines, "")
		for _, hint := range hints {
			lines = append(lines, "  "+hintStyle.Render(hint))
		}
	} else {
		// Returning user: a single dim hint line with the active context
		// (when known) plus the two slash commands the operator is most
		// likely to want at session start.
		dim := lipgloss.NewStyle()
		if !m.noColor {
			dim = dim.Foreground(lipgloss.Color("8"))
		}
		segs := []string{}
		if m.lastContext != "" {
			segs = append(segs, "ctx="+m.lastContext)
		}
		segs = append(segs, "/setup", "/help")
		lines = append(lines, "  "+dim.Render(strings.Join(segs, "  ·  ")))
	}

	return strings.Join(lines, "\n")
}

func (m WelcomeModel) renderCompactBanner() string {
	version := buildinfo.Version

	if m.noColor {
		parts := []string{"cloudy " + version}
		if m.lastContext != "" {
			parts = append(parts, "ctx="+m.lastContext)
		}
		parts = append(parts, "/help", "/setup")
		return strings.Join(parts, " · ")
	}

	// Brand the "cloudy <ver>" segment in sky blue; keep the rest dim so the
	// compact banner looks deliberate rather than uniformly grey.
	brand := lipgloss.NewStyle().
		Foreground(lipgloss.Color("117")).Bold(true).
		Render("cloudy " + version)

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sep := dim.Render(" · ")

	rest := []string{}
	if m.lastContext != "" {
		rest = append(rest, "ctx="+m.lastContext)
	}
	rest = append(rest, "/help", "/setup")
	return brand + sep + dim.Render(strings.Join(rest, " · "))
}
