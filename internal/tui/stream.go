package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tickInterval is the cadence at which the in-flight tool header is rewritten
// with an updated elapsed-seconds counter.
const tickInterval = time.Second

// streamFlushInterval is the upper bound on how often pending tokens are
// flushed into the viewport. 16ms ≈ one 60Hz frame: tokens that arrive
// within the same frame coalesce into a single viewport reflow, which is
// what eliminates the "툭툭" stutter the user reported. Without this,
// every Anthropic SSE chunk (typically 1–3 chars) triggered its own
// vp.SetContent + GotoBottom + View pass, and on long replies the per-
// token cost grew linearly because SetContent rebuilds line state from a
// fresh copy of the whole accumulated buffer.
const streamFlushInterval = 16 * time.Millisecond

// streamToolTickMsg is delivered once per tickInterval while a tool call is
// in flight, prompting the stream model to refresh the header's [MM:SS] suffix.
type streamToolTickMsg struct{}

// streamFlushTickMsg fires once after streamFlushInterval whenever there
// are pending tokens to flush. The stream model self-arms it on the
// first token write; subsequent tokens that arrive while a flush is
// already scheduled just append to the buffer without rescheduling.
type streamFlushTickMsg struct{}

// streamTokenMsg carries an LLM-streamed text fragment that the stream
// model intentionally batches via streamFlushTickMsg. Use this ONLY for
// the agent's flowing prose where dozens of tiny chunks per second would
// otherwise reflow the viewport on every token. UI chrome (echoes,
// error lines, command output) uses streamWriteMsg instead so it lands
// synchronously and tests can observe it without having to drive the
// flush tick.
type streamTokenMsg string

// streamWriteMsg is the immediate-write counterpart. Any pending agent
// tokens are drained first so writes stay in the order they were issued;
// then the payload is appended directly to the content buffer and the
// viewport is refreshed in the same Update. The split keeps the test
// suite straightforward (no need to pump a flush tick to read chrome
// output) and keeps user-facing diagnostics from being delayed by a
// frame.
type streamWriteMsg string

// streamToolBeginMsg signals the start of a tool call block.
type streamToolBeginMsg struct {
	name string
	args string
}

// streamToolEndMsg signals the end of a tool call block.
type streamToolEndMsg struct {
	observation string
	err         error
}

// streamClearMsg clears the stream viewport.
type streamClearMsg struct{}

// toolBlock tracks fold state for a tool call.
type toolBlock struct {
	name        string
	args        string
	observation string
	err         error
	folded      bool
}

// StreamModel backs the scrollable output area using a bubbles/viewport.
//
// content is a *strings.Builder, not a value, on purpose: the bubbletea
// Update contract returns a fresh StreamModel by value, and Go's runtime
// panics if a non-zero strings.Builder is copied. Holding the Builder
// behind a pointer means the copy carries only the pointer and every
// receiver writes to the same underlying buffer.
//
// pendingTokens and flushScheduled implement the frame-rate batching that
// fixes the streaming stutter: streamTokenMsg only appends to the buffer
// and schedules ONE streamFlushTickMsg if none is pending; the flush
// handler then drains the buffer in a single viewport SetContent + maybe
// GotoBottom pass. Same pointer rationale as content above.
type StreamModel struct {
	vp      viewport.Model
	content *strings.Builder
	ready   bool

	pendingTokens  *strings.Builder
	flushScheduled bool

	// pending tool block being assembled
	pendingTool *toolBlock

	// pendingStart is when the current tool call began; used to compute the
	// elapsed counter shown in the live header.
	pendingStart time.Time
	// pendingHeaderRaw is the most recently written unstyled header for the
	// in-flight tool; tickers rewrite this in content to refresh [MM:SS].
	pendingHeaderRaw string

	toolStyle lipgloss.Style
	obsStyle  lipgloss.Style
	errStyle  lipgloss.Style
	noColor   bool
}

// formatElapsed renders a duration as MM:SS (or HH:MM:SS past an hour).
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s/60)%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// renderToolHeader returns the unstyled header string for a tool call.
// Format matches Claude's CLI: a filled bullet, the tool name, the
// truncated args, and a parenthesised elapsed timer. Styling (sky-blue
// bullet + bold name + dim args/timer) is applied by the parent
// stream renderer; the unstyled form is what tick logic substitutes
// in/out of the content builder, so styles are re-applied on every
// refresh consistently.
func renderToolHeader(name, args string, elapsed time.Duration) string {
	return fmt.Sprintf("● %s(%s) (%s)", name, truncateToolArgs(args), formatElapsed(elapsed))
}

// truncateToolArgs keeps the args readable by clipping anything past
// ~60 chars and adding an ellipsis. Matches Claude's "Read(file.txt)"
// style — the operator sees what was called, not a JSON dump.
func truncateToolArgs(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// tickToolCmd returns a tea.Cmd that fires one streamToolTickMsg after
// tickInterval. Re-issued from Update on each tick while a tool is in flight.
func tickToolCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return streamToolTickMsg{} })
}

// streamFlushTickCmd schedules one streamFlushTickMsg ~one frame from
// now. Re-armed by the flush handler only when more tokens arrived
// during the window so an idle stream does not keep ticking.
func streamFlushTickCmd() tea.Cmd {
	return tea.Tick(streamFlushInterval, func(time.Time) tea.Msg { return streamFlushTickMsg{} })
}

func newStreamModel(noColor bool) StreamModel {
	s := StreamModel{
		noColor:       noColor,
		content:       &strings.Builder{},
		pendingTokens: &strings.Builder{},
	}
	if !noColor {
		s.toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		s.obsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		s.errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	}
	return s
}

// drainPending flushes any batched tokens into the live content buffer
// and refreshes the viewport once. Returns true when at least one byte
// was committed (callers use this to decide whether to GotoBottom).
//
// The "was at bottom" decision is captured BEFORE the new bytes land:
// the viewport's AtBottom() compares YOffset against the line count, so
// after SetContent the same offset would no longer be at the bottom and
// the heuristic would always return false on a previously-bottom stream.
func (s *StreamModel) drainPending() bool {
	if s.pendingTokens.Len() == 0 {
		return false
	}
	wasAtBottom := !s.ready || s.vp.AtBottom()
	s.content.WriteString(s.pendingTokens.String())
	s.pendingTokens.Reset()
	if s.ready {
		s.vp.SetContent(s.content.String())
		if wasAtBottom {
			s.vp.GotoBottom()
		}
	}
	return true
}

func (s StreamModel) Init() tea.Cmd { return nil }

func (s StreamModel) Update(msg tea.Msg) (StreamModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		// First WindowSizeMsg seeds the viewport so the very first frame
		// has something to render. The parent Model recomputes the exact
		// body height every View via SetViewportSize, accounting for the
		// prompt's border + an active palette + an approval banner — none
		// of which the stream can know about on its own.
		if !s.ready {
			s.vp = viewport.New(m.Width, m.Height)
			s.vp.SetContent(s.content.String())
			s.ready = true
		} else {
			s.vp.Width = m.Width
		}

	case streamTokenMsg:
		// Append-only: defer the viewport reflow to the next flush tick
		// so a burst of SSE chunks coalesces into one render. Schedule
		// the tick exactly once until the flush handler runs.
		s.pendingTokens.WriteString(string(m))
		if !s.flushScheduled {
			s.flushScheduled = true
			cmds = append(cmds, streamFlushTickCmd())
		}

	case streamFlushTickMsg:
		s.flushScheduled = false
		s.drainPending()

	case streamWriteMsg:
		// Synchronous write path for UI chrome (echoes, errors, command
		// output). Drain pending agent tokens first so the chrome line
		// follows whatever streaming text preceded it in submit order.
		s.drainPending()
		s.content.WriteString(string(m))
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}

	case streamToolBeginMsg:
		// Flush queued text before injecting structural markup so the
		// tool header lands strictly after the assistant prose that
		// preceded it, not interleaved with a half-rendered token batch.
		s.drainPending()
		s.pendingTool = &toolBlock{name: m.name, args: m.args}
		s.pendingStart = time.Now()
		s.pendingHeaderRaw = renderToolHeader(m.name, m.args, 0)
		header := s.pendingHeaderRaw
		if !s.noColor {
			header = s.toolStyle.Render(header)
		}
		s.content.WriteString("\n" + header + "\n")
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}
		// Start the elapsed-counter tick loop.
		cmds = append(cmds, tickToolCmd())

	case streamToolTickMsg:
		// No-op once the tool has ended — the loop stops by not re-issuing
		// the tick command from this branch.
		if s.pendingTool == nil {
			break
		}
		newRaw := renderToolHeader(s.pendingTool.name, s.pendingTool.args, time.Since(s.pendingStart))
		if newRaw != s.pendingHeaderRaw {
			oldRendered := s.pendingHeaderRaw
			newRendered := newRaw
			if !s.noColor {
				oldRendered = s.toolStyle.Render(s.pendingHeaderRaw)
				newRendered = s.toolStyle.Render(newRaw)
			}
			cur := strings.Replace(s.content.String(), oldRendered, newRendered, 1)
			s.content.Reset()
			s.content.WriteString(cur)
			s.pendingHeaderRaw = newRaw
			if s.ready {
				s.vp.SetContent(cur)
				s.vp.GotoBottom()
			}
		}
		cmds = append(cmds, tickToolCmd())

	case streamToolEndMsg:
		s.drainPending()
		if s.pendingTool != nil {
			s.pendingTool.observation = m.observation
			s.pendingTool.err = m.err
			s.pendingTool = nil
		}
		if m.err != nil {
			errLine := "  error: " + m.err.Error()
			if !s.noColor {
				errLine = s.errStyle.Render(errLine)
			}
			s.content.WriteString(errLine + "\n")
		}
		if m.observation != "" {
			obs := indentObs(m.observation)
			if !s.noColor {
				obs = s.obsStyle.Render(obs)
			}
			s.content.WriteString(obs + "\n")
		}
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}

	case streamClearMsg:
		s.content.Reset()
		s.pendingTokens.Reset()
		s.flushScheduled = false
		s.pendingTool = nil
		if s.ready {
			s.vp.SetContent("")
		}
	}

	if s.ready {
		s.vp, cmd = s.vp.Update(msg)
		cmds = append(cmds, cmd)
	}

	return s, tea.Batch(cmds...)
}

func (s StreamModel) View() string {
	if !s.ready {
		return ""
	}
	return s.vp.View()
}

// SetViewportSize lets the parent Model push an exact body width/height
// computed from the latest View pass. Needed because the stream cannot know
// how many rows the prompt border, palette, or approval banner consume.
// Width/height ≤ 0 are clamped to 1 to keep the viewport addressable.
func (s *StreamModel) SetViewportSize(width, height int) {
	if !s.ready {
		return
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if s.vp.Width != width {
		s.vp.Width = width
	}
	if s.vp.Height != height {
		s.vp.Height = height
	}
}

// Empty reports whether the stream has no content yet. Used by the parent
// Model to decide whether to render the welcome banner above the empty body.
// Pending (batched-but-not-yet-flushed) tokens count too so the banner
// vanishes on the very first user input even if the flush tick hasn't
// fired yet — otherwise the operator would briefly see the welcome
// banner re-appear over the in-flight assistant prefix.
func (s StreamModel) Empty() bool {
	return s.content.Len() == 0 && (s.pendingTokens == nil || s.pendingTokens.Len() == 0)
}

// indentObs renders a tool observation block in Claude's continuation
// style: the first non-empty line gets the "⎿  " branch glyph, every
// subsequent non-empty line is padded with three columns so the
// vertical rail stays straight. Empty lines are passed through so a
// result containing blank separators still reads correctly.
func indentObs(text string) string {
	const branch = "⎿  "
	const cont = "   " // same width as branch
	lines := strings.Split(text, "\n")
	first := true
	for i, l := range lines {
		if l == "" {
			continue
		}
		if first {
			lines[i] = branch + l
			first = false
			continue
		}
		lines[i] = cont + l
	}
	return strings.Join(lines, "\n")
}
