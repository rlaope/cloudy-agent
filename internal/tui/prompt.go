package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

const maxPromptLines = 6

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
	ta.SetHeight(3)
	ta.SetWidth(80)
	ta.ShowLineNumbers = false
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
			p.ta.SetHeight(3)
			return p, func() tea.Msg { return submitMsg(val) }

		case "shift+enter":
			if p.inSearch {
				return p, nil
			}
			// Insert newline and grow textarea up to max.
			p.ta, cmd = p.ta.Update(msg)
			lines := strings.Count(p.ta.Value(), "\n") + 1
			if lines < maxPromptLines {
				p.ta.SetHeight(lines + 1)
			}
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

	// Auto-detect slash prefix to signal palette.
	val := p.ta.Value()
	if strings.HasPrefix(val, "/") {
		// Palette open is handled at the app level by inspecting Value().
		_ = val
	}

	return p, cmd
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
	if p.inSearch {
		return "[search: " + p.searchBuf + "]\n" + p.ta.View()
	}
	return p.ta.View()
}

// Height returns the current textarea height in lines.
func (p PromptModel) Height() int {
	return p.ta.Height()
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
