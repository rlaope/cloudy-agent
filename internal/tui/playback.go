package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// playbackTickInterval is the cadence at which the typewriter playback
// pops runes off the buffer. 16ms ≈ one 60Hz frame; pairing this with
// playbackRunesPerTick gives a perceived char-rate of ~250/s — fast
// enough that long replies do not feel slow, smooth enough that the
// burstiness of Anthropic SSE chunks (1–3 chars at arbitrary times)
// is invisible to the operator.
const playbackTickInterval = 16 * time.Millisecond

// playbackRunesPerTick is the maximum number of runes emitted per
// playbackTickMsg. 2 runes / 16ms ≈ 125 chars/sec — slow enough to
// stay behind the LLM's production rate (typical Claude / GPT
// streaming runs 200-500 chars/sec) so the playback buffer keeps
// growing while the network is delivering. That growing buffer is
// what prevents the choppy "drain → pause → drain" pattern operators
// reported ("끊김"): when drain is slower than fill, the buffer
// never empties mid-stream and playback never stalls between SSE
// chunks. The trailing tail (when the LLM has finished but the
// buffer still has bytes left) plays out at the same steady cadence
// — a slight extension of total visible time in exchange for a
// continuous reading flow.
const playbackRunesPerTick = 2

// playbackToolFlushChunk caps the runes emitted in a single
// streamWriteMsg when a ToolBegin event forces an early drain.
// Splitting the drain into chunks rather than one giant write keeps
// terminal redraw cost bounded for very long buffered prefixes.
const playbackToolFlushChunk = 4096

// assistantContIndent is the continuation prefix injected after every
// newline inside an assistant response. Three spaces aligns the wrapped
// text with the column where the first character followed the "●  "
// bullet on the opening line — same visual rhythm as the "⎿  " /
// "   " pattern indentObs uses for tool observations.
const assistantContIndent = "   "

// playbackTickMsg fires once per playbackTickInterval while the
// playback buffer has bytes to drain.
type playbackTickMsg struct{}

// playbackTickCmd schedules one playbackTickMsg. The handler re-arms
// it only when more runes remain — an empty buffer lets the loop die.
func playbackTickCmd() tea.Cmd {
	return tea.Tick(playbackTickInterval, func(time.Time) tea.Msg { return playbackTickMsg{} })
}

// bufferAssistantToken queues the prose half of an LLM token for
// typewriter playback. Two contracts:
//
//  1. Every `\n` is followed by a fixed continuation indent
//     (assistantContIndent) so wrapped paragraphs read as a single
//     block, aligned under the bullet rather than flush left.
//  2. Runes are appended as []rune (not bytes) so the playback tick
//     pops a fixed number of *characters* per frame without ever
//     splitting a multi-byte UTF-8 sequence — Korean and emoji
//     survive intact.
//
// The styled "●  " bullet is NOT buffered here — it is emitted
// directly via streamWriteMsg by the caller so its ANSI escape
// sequences land atomically.
func (m *Model) bufferAssistantToken(token string) {
	for _, r := range token {
		m.playbackBuf = append(m.playbackBuf, r)
		if r == '\n' {
			m.playbackBuf = append(m.playbackBuf, []rune(assistantContIndent)...)
		}
	}
}

// drainPlaybackBuffer empties the entire playback buffer at once and
// returns the resulting string. Used by ToolBegin (where we don't
// want the tool block jumping ahead of the prose that intro'd it)
// and by hard-reset paths (cancel, clear, agentDoneMsg with error).
// Caller is responsible for writing the returned text to the stream.
func (m *Model) drainPlaybackBuffer() string {
	if len(m.playbackBuf) == 0 {
		return ""
	}
	out := string(m.playbackBuf)
	m.playbackBuf = m.playbackBuf[:0]
	return out
}

// popPlaybackRunes removes up to n runes from the front of the
// playback buffer and returns them as a string. Returns the empty
// string when the buffer is empty; safe to call when n exceeds
// buffer length (returns whatever was there).
func (m *Model) popPlaybackRunes(n int) string {
	if len(m.playbackBuf) == 0 {
		return ""
	}
	if n > len(m.playbackBuf) {
		n = len(m.playbackBuf)
	}
	head := string(m.playbackBuf[:n])
	m.playbackBuf = m.playbackBuf[n:]
	return head
}
