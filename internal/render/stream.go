package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// Stream is an incremental writer for LLM streaming output.  It writes
// directly to the underlying io.Writer on every call so that a long-running
// stream appears responsive in the terminal.
//
// Tool-call blocks are rendered as foldable-style sections:
//
//	▶ tool: jvm.jcmd_gc(pid=42)
//	  <observation text>
//
// The fold UI (collapse/expand) lives in the TUI layer; the renderer only
// produces the indented text blocks.
type Stream struct {
	w     io.Writer
	theme Theme

	// inTool tracks whether we are inside a BeginToolCall/EndToolCall pair.
	inTool bool

	// OnUsage, if non-nil, is called whenever usage data arrives in the stream.
	// It is intentionally a plain func so that callers (e.g. the TUI bridge)
	// can push tea.Msg values without creating an import cycle.
	OnUsage func(llm.Usage)
}

// NewStream creates a Stream that writes to w using the given Theme.
func NewStream(w io.Writer, theme Theme) *Stream {
	return &Stream{w: w, theme: theme}
}

// RecordUsage invokes the OnUsage callback with u if the callback is set.
// It is safe to call even when OnUsage is nil.
func (s *Stream) RecordUsage(u llm.Usage) {
	if s.OnUsage != nil {
		s.OnUsage(u)
	}
}

// WriteToken appends a single token (typically a word or punctuation fragment)
// to the output.  The write is flushed immediately.
func (s *Stream) WriteToken(tok string) {
	s.write(tok)
}

// BeginToolCall emits the tool-call header line.
//
//	▶ tool: <name>(<args>)
//
// If args is empty the parentheses are still emitted.
func (s *Stream) BeginToolCall(name, args string) {
	s.inTool = true
	header := fmt.Sprintf("▶ tool: %s(%s)", name, args)
	if !s.theme.NoColor() {
		header = s.theme.Hi.Render(header)
	}
	s.write("\n" + header + "\n")
}

// EndToolCall emits the observation (result) of a completed tool call.
// If err is non-nil it is rendered in the Err colour before the observation.
func (s *Stream) EndToolCall(observation string, err error) {
	s.inTool = false
	if err != nil {
		errLine := fmt.Sprintf("  error: %v", err)
		if !s.theme.NoColor() {
			errLine = s.theme.Err.Render(errLine)
		}
		s.write(errLine + "\n")
	}
	if observation != "" {
		// Indent every line of the observation.
		indented := indentBlock(observation, "  ")
		s.write(indented + "\n")
	}
}

// Reset clears any in-progress tool-call state so the Stream can be reused
// for a new LLM turn without recreating it.
func (s *Stream) Reset() {
	s.inTool = false
}

// write is the single write path; it never returns an error to keep callers
// simple (terminal writes rarely fail and we cannot do anything useful if
// they do).
func (s *Stream) write(text string) {
	_, _ = io.WriteString(s.w, text)
	// Flush if the writer supports it (e.g. bufio.Writer).
	if f, ok := s.w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

// indentBlock prepends prefix to every non-empty line in text.
func indentBlock(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}
