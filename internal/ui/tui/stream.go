package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
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

	// mdBuf accumulates the raw markdown text of the CURRENT assistant
	// message. drainPending re-renders mdBuf[:mdCommitted] via glamour
	// (the full prefix up to the last sentence boundary) and replaces
	// the last mdTailLen bytes of `content` with the new output. The
	// buffer is reset by finalizeMarkdown on tool / chrome / clear
	// boundaries so the next assistant message starts a fresh block.
	//
	// mdCommitted advances only at sentence-terminator + whitespace
	// boundaries (or paragraph newlines). That keeps the output stream
	// feeling like sentence-by-sentence prose rather than token-by-token
	// jitter — tokens still arrive at SSE speed but the visible commit
	// happens in coherent chunks.
	mdBuf       *strings.Builder
	mdCommitted int
	mdTailLen   int

	// mdRenderer is reconstructed on every WindowSizeMsg so word-wrap
	// matches the current viewport width. noColor mode leaves it nil and
	// drainPending falls back to writing raw text.
	mdRenderer *glamour.TermRenderer

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
		mdBuf:         &strings.Builder{},
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
	// Sentence-batched commit: only advance the visible content when a
	// new sentence terminator has landed past the previous commit point.
	// Partial-sentence tokens stay in mdBuf silently — the operator sees
	// completed clauses appear in coherent chunks instead of a flicker
	// of partial words being re-rendered every 16 ms.
	raw := s.mdBuf.String()
	bound := lastSentenceEnd(raw, s.mdCommitted)
	if bound < 0 {
		// No new sentence yet — drop the pending-flush signal so we do
		// not re-trigger on the same buffer state. The next streamToken
		// will schedule another tick.
		s.pendingTokens.Reset()
		return false
	}
	wasAtBottom := !s.ready || s.vp.AtBottom()
	s.pendingTokens.Reset()
	s.mdCommitted = bound + 1
	s.applyMarkdownTail()
	if s.ready && wasAtBottom {
		s.vp.GotoBottom()
	}
	return true
}

// applyMarkdownTail renders mdBuf[:mdCommitted] via glamour and
// replaces the last mdTailLen bytes of `content` with the new output.
// Shared by drainPending (after advancing mdCommitted), the resize
// re-render in WindowSizeMsg, and finalizeAssistantBlock (which forces
// the committed boundary all the way to mdBuf.Len()).
func (s *StreamModel) applyMarkdownTail() {
	raw := s.mdBuf.String()
	if s.mdCommitted > len(raw) {
		s.mdCommitted = len(raw)
	}
	if s.mdCommitted == 0 {
		return
	}
	rendered := s.renderAssistantTail(raw[:s.mdCommitted])
	cur := s.content.String()
	if s.mdTailLen > 0 && len(cur) >= s.mdTailLen {
		cur = cur[:len(cur)-s.mdTailLen]
	}
	s.content.Reset()
	s.content.WriteString(cur)
	s.content.WriteString(rendered)
	s.mdTailLen = len(rendered)
	if s.ready {
		s.vp.SetContent(s.content.String())
	}
}

// lastSentenceEnd returns the byte index in s of the last sentence
// terminator (`.`, `!`, `?`, `\n`) at or after minPos that is also
// followed by whitespace (or appears at end-of-string). The whitespace
// requirement avoids prematurely committing on abbreviations like
// "Mr." mid-stream — we wait until the next token confirms a space or
// newline follows. Returns -1 when no boundary is past minPos.
func lastSentenceEnd(s string, minPos int) int {
	if minPos < 0 {
		minPos = 0
	}
	for i := len(s) - 1; i >= minPos; i-- {
		c := s[i]
		switch c {
		case '\n':
			// Newlines are unambiguous boundaries — commit immediately
			// even at end-of-buffer.
			return i
		case '.', '!', '?', ',', ';', ':':
			// Sentence terminators AND clause separators count.
			// Committing on commas / colons gives clause-by-clause
			// cadence — sentence-only was too choppy in practice
			// (long sentences sat invisible for too long). The
			// followed-by-whitespace rule still applies so we don't
			// commit on numbers like `3.14` or `Mr.` mid-stream.
			if i+1 >= len(s) {
				continue
			}
			next := s[i+1]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' {
				return i
			}
		}
	}
	return -1
}

// RenderAssistantMarkdown is the public accessor for renderAssistantTail
// used by the post-done playback path in app.go: when an assistant turn
// finishes, the buffered raw markdown is rendered via glamour with the
// same width-aware renderer used for the in-stream tail, so the visible
// output gets headings/code/lists styled instead of the literal #/`/-
// syntax.
//
// Returns the input verbatim when noColor mode is active or the
// renderer hasn't been initialised yet — same fallback contract as the
// internal helper.
func (s *StreamModel) RenderAssistantMarkdown(raw string) string {
	return s.renderAssistantTail(raw)
}

// renderAssistantTail returns the glamour-rendered form of the current
// assistant message, or the raw text when noColor mode is set / the
// renderer isn't initialised yet (pre-first WindowSizeMsg). Glamour
// failures (rare, but possible on malformed markdown) fall back to raw
// so a broken render never blanks the stream.
func (s *StreamModel) renderAssistantTail(raw string) string {
	if raw == "" {
		return ""
	}
	if s.noColor || s.mdRenderer == nil {
		return raw
	}
	out, err := s.mdRenderer.Render(raw)
	if err != nil {
		return raw
	}
	return out
}

// finalizeAssistantBlock locks in the currently-rendered assistant tail
// as permanent content and resets the markdown buffer so the next
// assistant message starts a fresh block instead of overwriting this
// one's tail. Called at tool / chrome / clear boundaries.
//
// If sentence-batched commit left any tokens past mdCommitted (the
// message ended mid-sentence, e.g. an LLM that omits final punctuation
// or got cut off), force the commit boundary all the way to the end
// of mdBuf and apply one last render so the operator doesn't lose the
// trailing clause.
func (s *StreamModel) finalizeAssistantBlock() {
	if s.mdBuf.Len() > s.mdCommitted {
		s.mdCommitted = s.mdBuf.Len()
		s.applyMarkdownTail()
	}
	s.mdBuf.Reset()
	s.mdCommitted = 0
	s.mdTailLen = 0
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
		// Build / rebuild the glamour renderer so its word-wrap matches
		// the current viewport width. Auto-style picks dark/light based
		// on terminal background. noColor mode keeps the renderer nil so
		// drainPending writes raw markdown instead.
		if !s.noColor {
			wrap := m.Width - 2
			if wrap < 20 {
				wrap = 20
			}
			// Pin a static style instead of WithAutoStyle: auto-detect
			// fires an OSC 11 background-color query at the terminal,
			// and in inline mode (no alt-screen) the terminal's response
			// gets fed to stdin and lands in the prompt textarea as a
			// literal `]11;rgb:….` string. The dark style is the right
			// default for a developer terminal; light terminals can be
			// added behind an env var if anyone asks.
			r, err := glamour.NewTermRenderer(
				glamour.WithStandardStyle("dark"),
				glamour.WithWordWrap(wrap),
			)
			if err == nil {
				s.mdRenderer = r
				// If an assistant message is currently in flight,
				// re-render its committed prefix under the new width
				// immediately so the visible wrap matches the new
				// viewport. Uncommitted (mid-sentence) tail stays
				// hidden — applyMarkdownTail only re-renders up to
				// mdCommitted, matching the sentence-batched commit
				// model in drainPending.
				if s.mdCommitted > 0 {
					s.applyMarkdownTail()
				}
			}
		}

	case streamTokenMsg:
		// Append-only: defer the viewport reflow to the next flush tick
		// so a burst of SSE chunks coalesces into one render. Schedule
		// the tick exactly once until the flush handler runs. Tokens
		// land in BOTH pendingTokens (the flush-trigger signal) and
		// mdBuf (the rolling source for glamour re-renders).
		s.pendingTokens.WriteString(string(m))
		s.mdBuf.WriteString(string(m))
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
		// wasAtBottom is captured AFTER drainPending so it reflects the
		// viewport position once any pending text has landed but before
		// this chrome write extends content further. Operator-initiated
		// scrollback survives chrome events the same way it survives
		// streaming tokens.
		s.drainPending()
		// Commit the rendered assistant tail so this chrome line lands
		// strictly after it, not as a tail-replacement.
		s.finalizeAssistantBlock()
		wasAtBottom := !s.ready || s.vp.AtBottom()
		s.content.WriteString(string(m))
		if s.ready {
			s.vp.SetContent(s.content.String())
			if wasAtBottom {
				s.vp.GotoBottom()
			}
		}

	case streamToolBeginMsg:
		// Flush queued text before injecting structural markup so the
		// tool header lands strictly after the assistant prose that
		// preceded it, not interleaved with a half-rendered token batch.
		// Same scroll-position rule as streamWriteMsg — capture after
		// the drain so the operator's scrollback survives tool calls.
		s.drainPending()
		s.finalizeAssistantBlock()
		wasAtBottom := !s.ready || s.vp.AtBottom()
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
			if wasAtBottom {
				s.vp.GotoBottom()
			}
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
				// Header refresh is an in-place swap, not new content,
				// so respect the operator's scroll position the same
				// way the other write paths do.
				wasAtBottom := s.vp.AtBottom()
				s.vp.SetContent(cur)
				if wasAtBottom {
					s.vp.GotoBottom()
				}
			}
		}
		cmds = append(cmds, tickToolCmd())

	case streamToolEndMsg:
		// Same drain+capture pattern as streamToolBeginMsg / streamWriteMsg:
		// scroll-to-bottom is conditional on the operator already being
		// at the bottom, so a tool that finishes while the operator is
		// reading earlier output does not yank the viewport away.
		s.drainPending()
		s.finalizeAssistantBlock()
		wasAtBottom := !s.ready || s.vp.AtBottom()
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
			// Fold first so the rail glyph from indentObs is applied
			// after the head/tail/marker shape is decided. Otherwise
			// the "[… hidden …]" marker would land without the leading
			// indent and visually break the continuation rail.
			obs := indentObs(foldLongObservation(m.observation))
			if !s.noColor {
				obs = s.obsStyle.Render(obs)
			}
			s.content.WriteString(obs + "\n")
		}
		if s.ready {
			s.vp.SetContent(s.content.String())
			if wasAtBottom {
				s.vp.GotoBottom()
			}
		}

	case streamClearMsg:
		s.content.Reset()
		s.pendingTokens.Reset()
		s.mdBuf.Reset()
		s.mdCommitted = 0
		s.mdTailLen = 0
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
//
// pendingTokens is always initialised by newStreamModel, so a nil check
// here would be dead defensive code — every constructed StreamModel has
// a non-nil pointer.
func (s StreamModel) Empty() bool {
	return s.content.Len() == 0 && s.pendingTokens.Len() == 0
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

// foldObsLineLimit is the upper bound on lines emitted for a single
// tool observation. Beyond this the head + tail are kept and the
// middle is replaced with a hidden-line summary. Sized to give the
// operator a sense of the shape (first few rows, last few rows) without
// drowning the transcript when a single prom.query returns 5,000 rows
// or a log.tail dumps a 50 MB error log.
const foldObsLineLimit = 24

// foldObsHeadTail is the number of lines preserved from each end when
// folding kicks in. Keeping a chunk from both ends — rather than just
// the head — preserves the "what was the failure?" answer for long
// stack traces and the "what's the latest?" answer for log tails.
const foldObsHeadTail = 10

// foldLongObservation collapses an observation that is taller than
// foldObsLineLimit by retaining the head + tail and replacing the
// middle with a "[… N more lines hidden …]" marker. Short observations
// pass through unchanged. The marker line is intentionally unstyled
// here — indentObs will run after this and apply the rail glyph;
// styling happens in the parent Update.
func foldLongObservation(text string) string {
	lines := strings.Split(text, "\n")
	// Trailing-newline normalisation: log dumps, stack traces, and
	// most command output end with "\n", which strings.Split turns
	// into a trailing empty entry. Counting it would inflate the
	// "N more lines hidden" marker by 1 and shove a stray blank row
	// into the rendered tail. Strip it before the count, then re-add
	// at the end so the output preserves the input's terminator.
	hasTrailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if hasTrailingNewline {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= foldObsLineLimit {
		return text
	}
	// Defensive guard for future re-tuning: if foldObsLineLimit ever
	// drops below 2*foldObsHeadTail, head and tail would overlap and
	// the fold would emit duplicated rows under a nonsense marker.
	// Today the constants are 24 and 10 so this never triggers; keep
	// the invariant explicit so the assumption is not silently lost.
	if len(lines) <= foldObsHeadTail*2 {
		return text
	}
	hidden := len(lines) - (foldObsHeadTail * 2)
	head := lines[:foldObsHeadTail]
	tail := lines[len(lines)-foldObsHeadTail:]
	marker := fmt.Sprintf("[… %d more lines hidden …]", hidden)
	out := make([]string, 0, foldObsHeadTail*2+1)
	out = append(out, head...)
	out = append(out, marker)
	out = append(out, tail...)
	joined := strings.Join(out, "\n")
	if hasTrailingNewline {
		joined += "\n"
	}
	return joined
}
