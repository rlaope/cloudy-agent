package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// headerCompactThreshold is the terminal width below which the header
// switches to the abbreviated single-line form to avoid wrap/truncation
// when the user runs cloudy in a narrow split pane.
const headerCompactThreshold = 100

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
		// Background fill removed — the edge-to-edge dark block read
		// as heavy chrome and crowded the body. A dim foreground with
		// modest left padding lands closer to Claude's CLI status line:
		// quiet enough to ignore, present enough to glance at.
		h.style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			PaddingLeft(1)
		h.keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
		h.valStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
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
	var content string
	if h.width > 0 && h.width < headerCompactThreshold {
		// Compact form for narrow panes: drop fixed-width padding and ns/skill
		// segments so the line fits without wrap/truncation.
		content = fmt.Sprintf("ctx=%s  model=%s  $%.4f",
			shortField(h.ctx, 14), shortField(h.model, 18), h.cost)
		if h.scope != "" {
			content += "  s=" + shortField(h.scope, 10)
		}
	} else {
		content = fmt.Sprintf(
			"ctx=%-12s  ns=%-12s  model=%-28s  skill=%-14s  $%.4f",
			h.ctx, h.ns, h.model, h.skill, h.cost,
		)
		if h.scope != "" {
			content += "  scope=" + h.scope
		}
	}

	if h.noColor {
		return content
	}
	// No .Width(h.width) — the old full-width fill rendered a long
	// dark band across the top; the dim-text approach reads as a
	// status caption instead of a UI bar.
	return h.style.Render(content)
}

// shortField returns s truncated to at most max runes, appending a single
// horizontal ellipsis when the input is longer. Used by the compact-mode
// header so each segment has a hard upper bound.
//
// Rune-counted, not byte-counted: a Korean kubeconfig context name like
// "프로덕션" is 12 bytes but 4 runes; byte slicing would cut mid-rune and
// emit invalid UTF-8 to the terminal.
func shortField(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
