package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// playbackTickInterval is the cadence at which the look-ahead
// typewriter pops runes off the buffer. 16ms ≈ one 60Hz frame; paired
// with playbackRunesPerTick this gives a steady character-flow rate
// that the operator perceives as smooth typing rather than bursty SSE
// arrival. The look-ahead guarantee (see playbackEmittable) ensures
// the typewriter never stalls mid-sentence — a fresh sentence is only
// released for draining once its terminator has landed.
const playbackTickInterval = 16 * time.Millisecond

// playbackRunesPerTick is the per-frame rune budget. 2 runes / 16ms
// ≈ 125 chars/sec — slow enough that an LLM streaming at 200-500
// chars/sec keeps the emittable window ahead of the typewriter, so
// once a sentence starts appearing it flows continuously instead of
// stuttering between SSE chunks. Faster rates emptied the buffer mid-
// stream and produced the "끊김" pattern the previous tuning fought.
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
// typewriter playback. Three contracts:
//
//  1. Every `\n` is followed by a fixed continuation indent
//     (assistantContIndent) so wrapped paragraphs read as a single
//     block, aligned under the bullet rather than flush left.
//  2. Runes are appended as []rune (not bytes) so the playback tick
//     pops a fixed number of *characters* per frame without ever
//     splitting a multi-byte UTF-8 sequence — Korean and emoji
//     survive intact.
//  3. After appending, playbackEmittable is advanced to the last
//     sentence boundary present in the buffer. The tick handler only
//     drains runes up to that point, so once the typewriter starts
//     emitting a sentence it is guaranteed to have the entire sentence
//     buffered — no mid-sentence stalls between SSE chunks.
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
	m.refreshEmittableWindow()
}

// refreshEmittableWindow scans from the current emittable point to
// the end of the buffer and advances playbackEmittable to the
// furthest safe-to-emit boundary it finds. Boundaries are:
//
//   - any '\n' — always a unit terminator
//   - '.', '!', '?', ',', ';', ':' followed by whitespace — phrase
//     or sentence boundary
//
// Including clause separators (not just sentence terminators) keeps
// the emittable window almost always ahead of the typewriter: even
// inside a long sentence the window advances every few words, so the
// drain rate stays continuous. The "do not split mid-sentence" promise
// is preserved because the typewriter still drains chars at a steady
// 125 chars/sec — slower than LLM production, so the visible flow is
// uninterrupted across boundaries; the only thing the window prevents
// is leaping ahead before the buffered prefix forms a coherent unit.
func (m *Model) refreshEmittableWindow() {
	for i := m.playbackEmittable; i < len(m.playbackBuf); i++ {
		r := m.playbackBuf[i]
		if r == '\n' {
			m.playbackEmittable = i + 1
			continue
		}
		switch r {
		case '.', '!', '?', ',', ';', ':':
			if i+1 >= len(m.playbackBuf) {
				continue
			}
			next := m.playbackBuf[i+1]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' {
				m.playbackEmittable = i + 2
			}
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
		m.playbackEmittable = 0
		return ""
	}
	out := string(m.playbackBuf)
	m.playbackBuf = m.playbackBuf[:0]
	m.playbackEmittable = 0
	return out
}

// popPlaybackRunes removes up to n runes from the front of the
// playback buffer, capped by the emittable window. Returns the empty
// string when the window is closed (no complete sentence available)
// or the buffer is empty. Safe to call when n exceeds available
// runes; returns whatever was emittable.
func (m *Model) popPlaybackRunes(n int) string {
	if len(m.playbackBuf) == 0 || m.playbackEmittable == 0 {
		return ""
	}
	if n > m.playbackEmittable {
		n = m.playbackEmittable
	}
	head := string(m.playbackBuf[:n])
	m.playbackBuf = m.playbackBuf[n:]
	m.playbackEmittable -= n
	return head
}

// releasePlaybackTail forces the emittable window to cover the entire
// buffer. Called when the agent has signalled Done so a trailing
// partial sentence (LLM omitted final punctuation, or got cut off)
// still drains at typewriter pace instead of being stranded behind
// the look-ahead guard.
func (m *Model) releasePlaybackTail() {
	m.playbackEmittable = len(m.playbackBuf)
}
