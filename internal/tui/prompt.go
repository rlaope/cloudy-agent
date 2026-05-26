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
// Border colour is bright white (15) so the input box reads as the focus
// surface in the TUI, contrasted against the dim chrome elsewhere.
// Padding(1, 0, 0, 0) adds one blank row above the textarea content
// (between the top rule and the input line) so the prompt has a small
// breath of separation from the stream above without stealing a
// second row from the viewport. Bottom padding stays zero — the
// cursor line itself reads as the breathing room below.
var promptBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), true, false, true, false).
	BorderForeground(lipgloss.Color("15")).
	Padding(1, 0, 0, 0)

// promptBorderInFlightStyle is the same border drawn in the brand
// sky-blue so the operator gets an at-a-glance "the system is working"
// cue without having to look up at the thinking row. Toggled by
// PromptModel.inFlight, which the parent flips on submitMsg / clears
// on agentDoneMsg / cancel.
var promptBorderInFlightStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), true, false, true, false).
	BorderForeground(lipgloss.Color("117")).
	Padding(1, 0, 0, 0)

// promptBorderHeight is the number of extra terminal rows the border
// styles add: top rule + top pad + bottom rule = 3. Used by the
// parent Model for layout math; bump in lockstep with the Padding
// above if anyone re-tunes the breathing room.
const promptBorderHeight = 3

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

	// inFlight toggles the prompt's border color while an agent run is
	// in progress. Driven by the parent Model via SetInFlight on
	// submitMsg / agentDoneMsg / cancel paths.
	inFlight bool

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

	// Render the user's typed text and the "> " chevron in bright white.
	// Placeholder stays dim so the box reads as "empty waiting input" until
	// the operator types. Cursor-line styling is left as default — most
	// terminals already show a sensible focus block via the cursor render.
	white := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	ta.FocusedStyle.Text = white
	ta.FocusedStyle.Prompt = white
	ta.FocusedStyle.Placeholder = dim
	ta.BlurredStyle.Text = white
	ta.BlurredStyle.Prompt = white
	ta.BlurredStyle.Placeholder = dim

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
//
// Also syncs the placeholder so a returning operator who has typed at
// least one prompt this session sees "ask cloudy… (↑ for history)" —
// surfacing the up-arrow recall affordance that was previously
// invisible. Without this, new users had no way to discover history
// navigation short of reading /help.
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

	want := "ask cloudy…"
	if len(p.history) > 0 {
		want = "ask cloudy…   ↑ for history"
	}
	if p.ta.Placeholder != want {
		p.ta.Placeholder = want
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

// SetInFlight toggles the prompt border color so the operator gets a
// peripheral cue that the system is working. The parent calls this on
// submitMsg (true), agentDoneMsg (false), and on Esc/Ctrl+C cancel.
func (p *PromptModel) SetInFlight(v bool) { p.inFlight = v }

func (p PromptModel) View() string {
	var inner string
	if p.inSearch {
		inner = "[search: " + p.searchBuf + "]\n" + p.ta.View()
	} else {
		inner = p.ta.View()
	}
	style := promptBorderStyle
	if p.inFlight {
		style = promptBorderInFlightStyle
	}
	return style.Render(inner)
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
