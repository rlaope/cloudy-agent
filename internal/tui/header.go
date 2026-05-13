package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// headerStateMsg is sent to update header fields dynamically.
type headerStateMsg struct {
	ctx   string
	ns    string
	model string
	skill string
	cost  float64
	scope string // non-empty replaces the scope segment; "-" clears it
}

// HeaderModel renders the single-line status bar at the top of the TUI.
type HeaderModel struct {
	ctx   string
	ns    string
	model string
	skill string
	scope string // compact scope segment, e.g. "ns:payments  ctx:prod-eu"
	cost  float64
	width int

	style    lipgloss.Style
	keyStyle lipgloss.Style
	valStyle lipgloss.Style
	noColor  bool
}

// newHeaderModel constructs a HeaderModel with initial values.
func newHeaderModel(ctx, ns, model string) HeaderModel {
	noColor := os.Getenv("NO_COLOR") != ""
	h := HeaderModel{
		ctx:     ctx,
		ns:      ns,
		model:   model,
		skill:   "none",
		noColor: noColor,
	}
	if !noColor {
		h.style = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252")).
			PaddingLeft(1).
			PaddingRight(1)
		h.keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Inherit(h.style)
		h.valStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Bold(true).
			Inherit(h.style)
	}
	return h
}

func (h HeaderModel) Init() tea.Cmd { return nil }

func (h HeaderModel) Update(msg tea.Msg) (HeaderModel, tea.Cmd) {
	switch m := msg.(type) {
	case headerStateMsg:
		if m.ctx != "" {
			h.ctx = m.ctx
		}
		if m.ns != "" {
			h.ns = m.ns
		}
		if m.model != "" {
			h.model = m.model
		}
		if m.skill != "" {
			h.skill = m.skill
		}
		if m.scope == "-" {
			h.scope = ""
		} else if m.scope != "" {
			h.scope = m.scope
		}
		h.cost += m.cost
	case tea.WindowSizeMsg:
		h.width = m.Width
	}
	return h, nil
}

func (h HeaderModel) View() string {
	content := fmt.Sprintf(
		"ctx=%-12s  ns=%-12s  model=%-28s  skill=%-14s  $%.4f",
		h.ctx, h.ns, h.model, h.skill, h.cost,
	)
	if h.scope != "" {
		content += "  scope=" + h.scope
	}
	if h.noColor {
		return content
	}
	s := h.style.Width(h.width)
	return s.Render(content)
}
