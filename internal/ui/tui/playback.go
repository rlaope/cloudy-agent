package tui

import (
	"strings"
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

// playbackRunesPerTick is the per-frame rune budget. 24 runes / 16ms
// ≈ 1500 chars/sec — visibly a typewriter ("text flows in") rather
// than an instant dump, but fast enough that a few-hundred-token
// reply finishes in under a second. The tick only runs *after*
// agentDoneMsg has arrived, so there is no look-ahead worry about
// the emittable window catching the LLM's SSE rate; the whole
// buffer is already complete and released at full length via
// releasePlaybackTail.
const playbackRunesPerTick = 24

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
// typewriter playback. The buffer stores raw markdown (`#`, `**`,
// “ ` “, list dashes) verbatim — agentDoneMsg later runs the
// accumulated text through glamour so the operator sees rendered
// headings / bold / code / lists rather than the literal syntax.
//
// Runes are appended as []rune (not bytes) so the playback tick can
// pop a fixed number of *characters* per frame without ever splitting
// a multi-byte UTF-8 sequence — Korean and emoji survive intact.
//
// The styled "●  " bullet is NOT buffered here — it is emitted
// directly via streamWriteMsg by the caller so its ANSI escape
// sequences land atomically. Continuation indenting for wrapped
// lines is applied AFTER glamour renders, via indentRenderedBlock,
// so glamour does not misinterpret the leading whitespace as a
// fenced code block.
func (m *Model) bufferAssistantToken(token string) {
	m.playbackBuf = append(m.playbackBuf, []rune(token)...)
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

// popPlaybackRunes removes up to n VISIBLE runes from the front of
// the playback buffer, capped by the emittable window. ANSI escape
// sequences (CSI: ESC `[` … final byte 0x40-0x7E) are walked atomically
// and always emitted as part of the returned chunk, never split — so
// the post-glamour rendered output (which is text + interleaved
// `\x1b[…m` styling) typewriter-replays without ever sending a
// half-escape that would garble the terminal.
//
// Returns the empty string when the buffer is empty or the emittable
// window is closed.
func (m *Model) popPlaybackRunes(n int) string {
	if len(m.playbackBuf) == 0 || m.playbackEmittable == 0 {
		return ""
	}
	limit := m.playbackEmittable
	if limit > len(m.playbackBuf) {
		limit = len(m.playbackBuf)
	}
	visible := 0
	i := 0
	for i < limit && visible < n {
		r := m.playbackBuf[i]
		if r == 0x1b { // ESC: emit the whole escape sequence atomically
			i++
			if i < limit && m.playbackBuf[i] == '[' {
				i++
				for i < limit {
					c := m.playbackBuf[i]
					i++
					if c >= '@' && c <= '~' {
						break
					}
				}
			}
			continue
		}
		visible++
		i++
	}
	head := string(m.playbackBuf[:i])
	m.playbackBuf = m.playbackBuf[i:]
	if m.playbackEmittable >= i {
		m.playbackEmittable -= i
	} else {
		m.playbackEmittable = 0
	}
	return head
}

// indentRenderedBlock prepends assistantContIndent (three spaces) to
// every non-empty line of s except the first. The first line follows
// the "●  " bullet directly; subsequent lines wrap under the column
// the bullet established, giving the assistant block a clean left
// edge. ANSI escape sequences are preserved verbatim — `\n` is the
// only byte the walk reacts to and escape sequences never contain
// newlines, so the split is safe.
func indentRenderedBlock(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		lines[i] = assistantContIndent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// releasePlaybackTail forces the emittable window to cover the entire
// buffer. Called when the agent has signalled Done so a trailing
// partial sentence (LLM omitted final punctuation, or got cut off)
// still drains at typewriter pace instead of being stranded behind
// the look-ahead guard.
func (m *Model) releasePlaybackTail() {
	m.playbackEmittable = len(m.playbackBuf)
}
