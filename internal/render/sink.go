package render

import "github.com/rlaope/cloudy/internal/core/llm"

// Sink consumes the streaming events emitted by the agent loop. It is the
// seam between the agent (which speaks WriteToken / BeginToolCall / ...)
// and the actual presentation surface.
//
// Two canonical implementations exist today:
//
//   - *Stream in this package writes directly to an io.Writer for one-shot
//     CLI usage (cloudy ask).
//   - The TUI layer (internal/tui) supplies its own Sink that converts the
//     callbacks into bubbletea messages, removing the previous "wrap an
//     io.Writer and parse the markers back out" indirection.
//
// A nil Sink is valid and means "discard everything"; the agent calls
// each method only when the sink is non-nil.
type Sink interface {
	WriteToken(s string)
	BeginToolCall(name, args string)
	EndToolCall(observation string, err error)
	RecordUsage(u llm.Usage)
}

// Compile-time assertion that *Stream satisfies Sink.
var _ Sink = (*Stream)(nil)
