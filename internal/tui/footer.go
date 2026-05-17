package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Sentinel values for the footer's state and model segments. Promoted
// from inline strings so any drift in the wording (e.g. translation)
// happens in one place.
const (
	footerStateReady        = "set-up done"
	footerStateUnconfigured = "no set-up"
	footerSeparator         = " | "
)

// FooterModel renders the single-line status bar shown directly below
// the prompt: `cloudy <ver> | state: <state> | model: <id>`.
//
// Styles and the immutable brand segment are precomputed in
// NewFooterModel; View() only does the cheap mutable-segment Render
// calls. The parent owns version (passed in) and the model id so the
// footer never reads buildinfo directly — keeps the seam testable and
// the single source of truth in the parent Model.
type FooterModel struct {
	state string
	model string
	width int

	brandRendered string // pre-rendered "cloudy <ver>" segment
	sepRendered   string // pre-rendered separator with dim style
	labelStyle    lipgloss.Style
	valueStyle    lipgloss.Style
	noColor       bool
}

// NewFooterModel constructs a FooterModel. Empty state/model both
// fall back to footerStateUnconfigured ("no set-up") so the bar never
// shows a blank segment.
func NewFooterModel(state, model, version string) FooterModel {
	noColor := os.Getenv("NO_COLOR") != ""
	f := FooterModel{
		state:   orUnconfigured(state),
		model:   orUnconfigured(model),
		width:   80,
		noColor: noColor,
	}
	if noColor {
		f.brandRendered = "cloudy " + version
		f.sepRendered = footerSeparator
	} else {
		f.brandRendered = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117")).Bold(true).
			Render("cloudy " + version)
		f.sepRendered = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Render(footerSeparator)
		f.labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		f.valueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	}
	return f
}

// SetWidth lets the parent push the latest terminal width.
func (f *FooterModel) SetWidth(w int) { f.width = w }

// SetState updates the setup-state segment shown by the footer.
func (f *FooterModel) SetState(s string) { f.state = orUnconfigured(s) }

// SetModel updates the model segment.
func (f *FooterModel) SetModel(m string) { f.model = orUnconfigured(m) }

// View renders the single-line footer.
func (f FooterModel) View() string {
	if f.noColor {
		return f.brandRendered + f.sepRendered +
			"state: " + f.state + f.sepRendered +
			"model: " + f.model
	}
	var b strings.Builder
	b.Grow(len(f.brandRendered) + 64)
	b.WriteString(f.brandRendered)
	b.WriteString(f.sepRendered)
	b.WriteString(f.labelStyle.Render("state: "))
	b.WriteString(f.valueStyle.Render(f.state))
	b.WriteString(f.sepRendered)
	b.WriteString(f.labelStyle.Render("model: "))
	b.WriteString(f.valueStyle.Render(f.model))
	return b.String()
}

// orUnconfigured maps empty input to footerStateUnconfigured so every
// footer setter goes through the same fallback rule.
func orUnconfigured(s string) string {
	if s == "" {
		return footerStateUnconfigured
	}
	return s
}
