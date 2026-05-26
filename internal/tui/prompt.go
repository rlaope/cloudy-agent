package tui

import (
	"encoding/base64"
	"os"
	"strings"
	"unicode/utf8"

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
var promptBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), true, false, true, false).
	BorderForeground(lipgloss.Color("15"))

// promptBorderInFlightStyle is the same border drawn in the brand
// sky-blue so the operator gets an at-a-glance "the system is working"
// cue without having to look up at the thinking row. Toggled by
// PromptModel.inFlight, which the parent flips on submitMsg / clears
// on agentDoneMsg / cancel.
var promptBorderInFlightStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), true, false, true, false).
	BorderForeground(lipgloss.Color("117"))

// promptBorderHeight is the number of extra terminal rows the border
// styles add: top rule + bottom rule = 2. Bump if any future change
// re-introduces vertical padding inside the rules.
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

	// inFlight toggles the prompt's border color while an agent run is
	// in progress. Driven by the parent Model via SetInFlight on
	// submitMsg / agentDoneMsg / cancel paths.
	inFlight bool

	// selAnchor is the rune offset (in textarea.Value()) where Shift+arrow
	// selection began. -1 means no active selection. Selection extends
	// from selAnchor to the textarea's current cursor position; Ctrl+Y
	// copies the range to the system clipboard via OSC 52, any non-shift
	// key clears the anchor. Bubbles v1.0.0's textarea has no built-in
	// selection, so this is a thin wrapper that intercepts the shift
	// arrows before forwarding their plain-arrow equivalents.
	selAnchor int

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
		ta:        ta,
		histIdx:   -1,
		selAnchor: -1,
		keys:      keys,
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
		// Selection interception comes BEFORE the regular key switch so
		// shift+arrow / ctrl+y never reach the textarea (it doesn't
		// understand them). After this block, any other key clears the
		// selection anchor — typing or navigating exits selection mode
		// the same way it does in a GUI text field.
		switch m.String() {
		case "shift+left", "shift+right", "shift+up", "shift+down", "shift+home", "shift+end":
			if p.selAnchor < 0 {
				p.selAnchor = p.cursorRuneOffset()
			}
			plain := plainArrowKey(m.String())
			p.ta, cmd = p.ta.Update(tea.KeyMsg{Type: plain})
			return p, cmd
		case "ctrl+y":
			c := p.copySelectionCmd()
			p.selAnchor = -1
			return p, c
		}
		// Reached only for non-shift / non-copy keys. Clear the anchor so
		// typing or a plain arrow exits selection mode cleanly.
		p.selAnchor = -1

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
	switch {
	case p.inSearch:
		inner = "[search: " + p.searchBuf + "]\n" + p.ta.View()
	case p.selAnchor >= 0:
		// Replace the textarea's render entirely when a selection is
		// active so the selected runes appear with reverse video IN
		// PLACE (where they sit in the value), matching the visual
		// model of GUI text-editor drag selection. Bubbles can't
		// composite this kind of inline highlight on its own.
		inner = p.renderWithSelection()
	default:
		inner = p.ta.View()
	}
	style := promptBorderStyle
	if p.inFlight {
		style = promptBorderInFlightStyle
	}
	return style.Render(inner)
}

// renderWithSelection draws the prompt manually with the selected
// substring wrapped in lipgloss.Reverse so the operator sees the same
// "white block" feedback as a GUI text editor's drag selection. Per-
// line `>` chevrons are added so multi-line prompts still look like the
// textarea (the textarea inserts those internally). The cursor block
// and blink are intentionally not redrawn — the selection block
// already pins the operator's eye where the cursor is.
func (p PromptModel) renderWithSelection() string {
	val := p.ta.Value()
	runes := []rune(val)
	a, b := p.selAnchor, p.cursorRuneOffset()
	if a > b {
		a, b = b, a
	}
	if a < 0 {
		a = 0
	}
	if b > len(runes) {
		b = len(runes)
	}

	white := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	reverse := lipgloss.NewStyle().Reverse(true)

	before := string(runes[:a])
	sel := string(runes[a:b])
	after := string(runes[b:])

	var out strings.Builder
	out.WriteString(white.Render("> "))
	writeChevroned(&out, white, before, false)
	writeChevroned(&out, reverse, sel, true)
	writeChevroned(&out, white, after, true)
	return out.String()
}

// writeChevroned writes s to out under the given style, inserting
// "\n> " before every embedded newline so multi-line content lines up
// with the bubbles textarea's per-row chevron prefix. addInitialNL
// controls whether a leading "\n> " is emitted when s starts with text
// after a prior segment; it's set false for the very first segment of
// the prompt (where the parent already wrote "> ").
func writeChevroned(out *strings.Builder, style lipgloss.Style, s string, addInitialNL bool) {
	if s == "" {
		return
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 || (addInitialNL && line == "") {
			// Only emit a new chevron when there's an actual newline
			// boundary between segments; addInitialNL handles the
			// rare case where the previous segment ended mid-line
			// and this one begins on the same line.
		}
		if i > 0 {
			out.WriteString("\n")
			out.WriteString(style.Render("> "))
		}
		out.WriteString(style.Render(line))
	}
}

// cursorRuneOffset returns the textarea's current cursor position as a
// rune offset into Value(). Counts runes (not bytes) so multi-byte input
// (e.g. Korean) stays consistent with the selection range we slice from
// []rune(Value()) later.
func (p PromptModel) cursorRuneOffset() int {
	row := p.ta.Line()
	li := p.ta.LineInfo()
	colInRow := li.StartColumn + li.ColumnOffset

	lines := strings.Split(p.ta.Value(), "\n")
	if row >= len(lines) {
		row = len(lines) - 1
	}
	if row < 0 {
		return 0
	}
	offset := 0
	for i := 0; i < row; i++ {
		offset += utf8.RuneCountInString(lines[i]) + 1 // +1 for the newline
	}
	if line := lines[row]; colInRow > utf8.RuneCountInString(line) {
		colInRow = utf8.RuneCountInString(line)
	}
	return offset + colInRow
}

// plainArrowKey maps a shift+arrow key string to its plain-arrow KeyType
// so the textarea (which has no selection-aware key bindings) still
// performs the cursor movement the operator expects while the prompt
// wrapper tracks the selection range separately.
func plainArrowKey(shiftKey string) tea.KeyType {
	switch shiftKey {
	case "shift+left":
		return tea.KeyLeft
	case "shift+right":
		return tea.KeyRight
	case "shift+up":
		return tea.KeyUp
	case "shift+down":
		return tea.KeyDown
	case "shift+home":
		return tea.KeyHome
	case "shift+end":
		return tea.KeyEnd
	}
	return tea.KeyNull
}

// copySelectionCmd writes the active selection to the system clipboard
// via an OSC 52 escape sequence. The escape is emitted to os.Stderr
// inside a tea.Cmd so bubble tea's renderer (which manages os.Stdout)
// doesn't see the write and doesn't desync its cursor-tracking state.
// Returns nil when there is no selection.
func (p PromptModel) copySelectionCmd() tea.Cmd {
	if p.selAnchor < 0 {
		return nil
	}
	cur := p.cursorRuneOffset()
	a, b := p.selAnchor, cur
	if a > b {
		a, b = b, a
	}
	runes := []rune(p.ta.Value())
	if a < 0 {
		a = 0
	}
	if b > len(runes) {
		b = len(runes)
	}
	if a >= b {
		return nil
	}
	text := string(runes[a:b])
	return func() tea.Msg {
		encoded := base64.StdEncoding.EncodeToString([]byte(text))
		// OSC 52 ("set clipboard") is parsed and consumed by the
		// terminal silently — no visible characters, no cursor
		// movement — so writing it from a goroutine outside bubble
		// tea's renderer pipeline is safe.
		_, _ = os.Stderr.WriteString("\x1b]52;c;" + encoded + "\x07")
		return nil
	}
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
