package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

// FooterModel renders the single-line status bar shown directly below the
// prompt — `cloudy <ver> | state: <state> | model: <id>`. It is a pure
// renderer: the parent Model owns version/state/model strings and the
// footer just formats them. Sky-blue brand on the version segment, dim
// grey separators and trailing labels so the bar reads as deliberate
// chrome rather than noise.
type FooterModel struct {
	version   string
	state     string // "set-up done", "no set-up", custom string
	model     string // current model id; "no set-up" when unset
	width     int
	noColor   bool
	separator string
}

// NewFooterModel constructs a FooterModel. Pass the dependency Model
// straight from the parent; version comes from buildinfo.
func NewFooterModel(state, model string) FooterModel {
	if state == "" {
		state = "no set-up"
	}
	if model == "" {
		model = "no set-up"
	}
	return FooterModel{
		version:   buildinfo.Version,
		state:     state,
		model:     model,
		width:     80,
		noColor:   os.Getenv("NO_COLOR") != "",
		separator: " | ",
	}
}

// SetWidth lets the parent push the latest terminal width.
func (f *FooterModel) SetWidth(w int) { f.width = w }

// SetState updates the setup-state segment shown by the footer.
// "" maps to "no set-up" so the footer never shows an empty field.
func (f *FooterModel) SetState(s string) {
	if s == "" {
		s = "no set-up"
	}
	f.state = s
}

// SetModel updates the model segment. "" maps to "no set-up".
func (f *FooterModel) SetModel(m string) {
	if m == "" {
		m = "no set-up"
	}
	f.model = m
}

// View renders the single-line footer.
func (f FooterModel) View() string {
	if f.noColor {
		return "cloudy " + f.version + f.separator +
			"state: " + f.state + f.separator +
			"model: " + f.model
	}

	brand := lipgloss.NewStyle().
		Foreground(lipgloss.Color("117")).Bold(true).
		Render("cloudy " + f.version)

	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(f.separator)

	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	var b strings.Builder
	b.WriteString(brand)
	b.WriteString(sep)
	b.WriteString(label.Render("state: "))
	b.WriteString(value.Render(f.state))
	b.WriteString(sep)
	b.WriteString(label.Render("model: "))
	b.WriteString(value.Render(f.model))
	return b.String()
}
