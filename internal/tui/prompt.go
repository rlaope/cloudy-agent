package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxPromptLines = 6

// promptBorderStyle draws a horizontal line above and below the textarea
// (Claude-style input box). Left/right borders are intentionally suppressed —
// the prompt fills the full terminal width with two clean rules framing it.
var promptBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), true, false, true, false).
	BorderForeground(lipgloss.Color("240"))

// promptBorderHeight is the number of extra terminal rows the border adds
// (top rule + bottom rule = 2). Used by the parent Model for layout math.
// Hard-coded to 2 because promptBorderStyle uses lipgloss.NormalBorder()
// with top+bottom only; bump if the border style ever grows extra rows.
const promptBorderHeight = 2

// submitMsg is sent when the user presses Enter on a non-slash prompt.
type submitMsg string

// PromptModel wraps a bubbles/textarea with history navigation and slash detection.
type PromptModel struct {
	ta      textarea.Model
	history []string
	histIdx int    // -1 means "not navigating history"; 0..len-1 means viewing history[idx]
	draft   string // saves current draft when entering history navigation

	// histSearch state for Ctrl+R
	inSearch   bool
	searchBuf  string
	searchIdx  int
	searchHits []string

	keys keyMap
}

func newPromptModel(keys keyMap) PromptModel {
	ta := textarea.New()
	ta.Placeholder = "ask cloudy…"
	ta.CharLimit = 0
	// Start at one row like Claude — grows as the operator inserts newlines.
	ta.SetHeight(1)
	ta.SetWidth(80)
	ta.ShowLineNumbers = false
	// Single-character chevron at the start of each line: matches Claude's
	// "> " input affordance. Two columns wide (chevron + space) so the
	// caret sits where the user expects.
	ta.Prompt = "> "
	ta.Focus()

	return PromptModel{
		ta:      ta,
		histIdx: -1,
		keys:    keys,
	}
}

func (p PromptModel) Init() tea.Cmd {
	return textarea.Blink
}

func (p PromptModel) Update(msg tea.Msg) (PromptModel, tea.Cmd) {
	var cmd tea.Cmd

	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		p.ta.SetWidth(m.Width)

	case tea.KeyMsg:
		switch m.String() {
		case "ctrl+r":
			// Toggle incremental history search.
			p.inSearch = !p.inSearch
			if p.inSearch {
				p.searchBuf = ""
				p.searchIdx = 0
				p.searchHits = nil
			}
			return p, nil

		case "esc":
			if p.inSearch {
				p.inSearch = false
				p.searchBuf = ""
				return p, nil
			}
			// Pass esc up for cancel handling.
			return p, nil

		case "enter":
			if p.inSearch {
				// Accept current search match.
				if len(p.searchHits) > 0 {
					p.ta.SetValue(p.searchHits[p.searchIdx])
				}
				p.inSearch = false
				return p, nil
			}

			val := strings.TrimSpace(p.ta.Value())
			if val == "" {
				return p, nil
			}
			// Slash commands go to palette, not submit.
			if strings.HasPrefix(val, "/") {
				return p, nil
			}
			// Add to history and emit.
			p.history = append(p.history, val)
			p.histIdx = -1
			p.draft = ""
			p.ta.SetValue("")
			p.ta.SetHeight(1) // collapse back to one row on submit
			return p, func() tea.Msg { return submitMsg(val) }

		case "shift+enter", "alt+enter", "ctrl+j":
			// Newline. Three keys are accepted because terminal emulators
			// disagree on which sequence Shift+Enter actually sends:
			//   - iTerm2 with "report modifiers" sends shift+enter literally.
			//   - macOS Terminal.app sends a bare CR (indistinguishable from
			//     plain Enter); Option+Enter is the realistic alternative.
			//   - tmux / older xterms expose Ctrl+J as the canonical "send
			//     a literal LF without submitting" sequence.
			if p.inSearch {
				return p, nil
			}
			// Forward an Alt+Enter event because bubbles/textarea binds
			// InsertNewline to that exact key combination; other newline-ish
			// shortcuts the operator pressed are routed through the same
			// canonical form so the textarea always sees something it knows
			// how to insert.
			p.ta, cmd = p.ta.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
			p.syncHeight()
			return p, cmd

		case "up":
			if p.inSearch {
				if len(p.searchHits) > 1 {
					p.searchIdx = (p.searchIdx + 1) % len(p.searchHits)
					p.ta.SetValue(p.searchHits[p.searchIdx])
				}
				return p, nil
			}
			if len(p.history) == 0 {
				return p, nil
			}
			if p.histIdx == -1 {
				p.draft = p.ta.Value()
				p.histIdx = len(p.history) - 1
			} else if p.histIdx > 0 {
				p.histIdx--
			}
			p.ta.SetValue(p.history[p.histIdx])
			return p, nil

		case "down":
			if p.inSearch {
				if len(p.searchHits) > 1 {
					p.searchIdx = (p.searchIdx - 1 + len(p.searchHits)) % len(p.searchHits)
					p.ta.SetValue(p.searchHits[p.searchIdx])
				}
				return p, nil
			}
			if p.histIdx == -1 {
				return p, nil
			}
			p.histIdx++
			if p.histIdx >= len(p.history) {
				p.histIdx = -1
				p.ta.SetValue(p.draft)
				return p, nil
			}
			p.ta.SetValue(p.history[p.histIdx])
			return p, nil

		default:
			if p.inSearch {
				// Accumulate search buffer from printable keys.
				if len(m.String()) == 1 {
					p.searchBuf += m.String()
					p.rebuildSearchHits()
					if len(p.searchHits) > 0 {
						p.ta.SetValue(p.searchHits[p.searchIdx])
					}
				} else if m.String() == "backspace" && len(p.searchBuf) > 0 {
					p.searchBuf = p.searchBuf[:len(p.searchBuf)-1]
					p.rebuildSearchHits()
					if len(p.searchHits) > 0 {
						p.ta.SetValue(p.searchHits[p.searchIdx])
					}
				}
				return p, nil
			}
		}
	}

	p.ta, cmd = p.ta.Update(msg)
	p.syncHeight()

	// Auto-detect slash prefix to signal palette.
	val := p.ta.Value()
	if strings.HasPrefix(val, "/") {
		// Palette open is handled at the app level by inspecting Value().
		_ = val
	}

	return p, cmd
}

// syncHeight resizes the textarea to match the current line count, bounded
// by [1, maxPromptLines]. Called after every Update so the input box grows
// (and shrinks) with the operator's typing — matching Claude's prompt UX.
func (p *PromptModel) syncHeight() {
	lines := strings.Count(p.ta.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	if lines > maxPromptLines {
		lines = maxPromptLines
	}
	if p.ta.Height() != lines {
		p.ta.SetHeight(lines)
	}
}

// Value returns the current textarea content.
func (p PromptModel) Value() string {
	return p.ta.Value()
}

// SetValue sets the textarea content.
func (p *PromptModel) SetValue(v string) {
	p.ta.SetValue(v)
}

// Focus focuses the textarea.
func (p *PromptModel) Focus() tea.Cmd {
	return p.ta.Focus()
}

func (p PromptModel) View() string {
	var inner string
	if p.inSearch {
		inner = "[search: " + p.searchBuf + "]\n" + p.ta.View()
	} else {
		inner = p.ta.View()
	}
	return promptBorderStyle.Render(inner)
}

// Height returns the prompt's full rendered height in terminal rows, including
// the top + bottom border lines that promptBorderStyle adds. Parent layout
// code subtracts this from the terminal height to size the stream viewport.
func (p PromptModel) Height() int {
	return p.ta.Height() + promptBorderHeight
}

func (p *PromptModel) rebuildSearchHits() {
	p.searchHits = nil
	p.searchIdx = 0
	lower := strings.ToLower(p.searchBuf)
	for i := len(p.history) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(p.history[i]), lower) {
			p.searchHits = append(p.searchHits, p.history[i])
		}
	}
}
