package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ansiSeqRe matches a CSI escape sequence (ESC `[` … final-byte). Used
// by the typewriter-drain tests to assert on the unstyled text content
// after agentDoneMsg has run the buffer through glamour.
var ansiSeqRe = regexp.MustCompile("\x1b\\[[0-9;]*[@-~]")

func stripANSI(s string) string {
	return ansiSeqRe.ReplaceAllString(s, "")
}

// TestPlayback_BuffersTokensSilentlyDuringStream pins the core
// contract of the new playback model: while the agent is still
// streaming, prose tokens are accumulated in playbackBuf and NOTHING
// lands in the stream content. The operator only sees the thinking
// row spinner until agentDoneMsg flushes the buffer.
func TestPlayback_BuffersTokensSilentlyDuringStream(t *testing.T) {
	deps := makeDeps()
	deps.Model = "claude-test"
	deps.SwapModel = func(string) error { return nil }
	deps.AgentRunner = func(cancel <-chan struct{}, _ string, emit func(AgentEvent)) {
		emit(AgentEvent{Token: "안녕하세요"})
	}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)
	next, _ = m.Update(submitMsg("hi"))
	m = next.(Model)

	if m.agentCh == nil {
		t.Fatal("submitMsg should install agentCh")
	}
	msg := <-m.agentCh
	next, _ = m.Update(agentEventMsg(msg))
	m = next.(Model)

	// Neither the bullet nor the prose lands while streaming: the
	// operator sees only the spinner. The bullet is deferred to
	// agentDoneMsg so it never sits alone on screen waiting for the
	// body to catch up; the prose stays queued in playbackBuf.
	if strings.Contains(m.stream.content.String(), "●") {
		t.Errorf("● bullet must NOT land during streaming; content: %q",
			m.stream.content.String())
	}
	if strings.Contains(m.stream.content.String(), "안녕") {
		t.Errorf("prose must NOT land while the agent is still streaming; content: %q",
			m.stream.content.String())
	}
	if len(m.playbackBuf) == 0 {
		t.Error("prose must be queued in playbackBuf for the eventual drain")
	}
	if m.assistantTurnStarted {
		t.Error("assistantTurnStarted must stay false until the bullet actually emits at Done")
	}
}

// TestPlayback_DoneStartsTypewriterDrain pins the post-done flush
// contract: agentDoneMsg does NOT dump the body in one go. It stops
// the spinner, releases the playback tail, and arms a tick loop that
// drains the buffer at typewriter pace until empty.
func TestPlayback_DoneStartsTypewriterDrain(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.running = true
	m.assistantTurnStarted = true
	m.bufferAssistantToken("complete response body here.")

	next, _ = m.Update(agentDoneMsg{})
	m = next.(Model)

	// Spinner/turn state retire immediately so the prompt comes back.
	if m.running {
		t.Error("agentDoneMsg must clear running")
	}
	if m.assistantTurnStarted {
		t.Error("agentDoneMsg must reset assistantTurnStarted for the next turn")
	}
	// The buffer is NOT drained synchronously any more.
	if len(m.playbackBuf) == 0 {
		t.Fatal("playback buffer should still hold the reply, queued for typewriter drain")
	}
	if !m.playbackActive {
		t.Error("playbackActive must be true so subsequent playbackTickMsg events drain")
	}
	if m.playbackEmittable != len(m.playbackBuf) {
		t.Errorf("releasePlaybackTail should open the full buffer; emittable=%d len=%d",
			m.playbackEmittable, len(m.playbackBuf))
	}

	// Drive ticks until the buffer drains. Bound the loop so a
	// regression that leaves the drain inactive fails the test
	// instead of spinning forever.
	const guard = 1000
	for i := 0; i < guard && len(m.playbackBuf) > 0; i++ {
		next, _ = m.Update(playbackTickMsg{})
		m = next.(Model)
	}
	if len(m.playbackBuf) != 0 {
		t.Errorf("playbackTickMsg loop never drained the buffer; left=%d", len(m.playbackBuf))
	}
	if m.playbackActive {
		t.Error("playbackActive must reset to false once the buffer is empty")
	}
	plain := strings.Join(strings.Fields(stripANSI(m.stream.content.String())), " ")
	if !strings.Contains(plain, "complete response body here.") {
		t.Errorf("body must eventually land in stream content; got %q", plain)
	}
}

// TestPlayback_BufferStoresRawMarkdown pins the new buffering
// contract: bufferAssistantToken does NOT inject continuation
// indents during streaming any more. Glamour parses the buffered
// content as markdown at agentDoneMsg time, and leading whitespace
// before a paragraph would be misinterpreted as a fenced code block.
// The continuation indent is reapplied to glamour's rendered output
// via indentRenderedBlock instead.
func TestPlayback_BufferStoresRawMarkdown(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.bufferAssistantToken("first\nsecond")
	if got := string(m.playbackBuf); got != "first\nsecond" {
		t.Errorf("playbackBuf must store raw tokens verbatim; got %q", got)
	}

	// indentRenderedBlock is what supplies the visual indent — applied
	// after glamour has parsed the markdown so no leading-whitespace
	// confusion is possible.
	if got := indentRenderedBlock("first\nsecond"); got != "first\n"+assistantContIndent+"second" {
		t.Errorf("indentRenderedBlock wrong; got %q", got)
	}
}

// TestPlayback_ToolBeginForcesImmediateDrain is the contract that
// stops a tool block from leapfrogging the prose that introduced it.
// When ToolBegin arrives mid-stream, the buffered prose flushes
// synchronously before the tool header lands — same as before the
// playback overhaul, since the typewriter never owned this guarantee.
func TestPlayback_ToolBeginForcesImmediateDrain(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.assistantTurnStarted = true
	m.bufferAssistantToken("checking pods now…")
	bufBefore := string(m.playbackBuf)
	if bufBefore == "" {
		t.Fatal("test setup: prose should be queued")
	}

	_ = m.applyAgentEvent(AgentEvent{ToolBegin: &toolBeginEvt{
		name: "k8s.get_pods",
		args: "{}",
	}})

	if len(m.playbackBuf) != 0 {
		t.Errorf("ToolBegin must flush playbackBuf; got leftover len=%d", len(m.playbackBuf))
	}
	content := m.stream.content.String()
	if !strings.Contains(content, "checking pods now") {
		t.Errorf("prose must land in stream before tool header; content: %q", content)
	}
	proseIdx := strings.Index(content, "checking pods now")
	toolIdx := strings.Index(content, "k8s.get_pods")
	if proseIdx == -1 || toolIdx == -1 {
		t.Fatalf("both prose and tool header should be present; content: %q", content)
	}
	if proseIdx >= toolIdx {
		t.Errorf("prose must appear before tool header; prose=%d tool=%d", proseIdx, toolIdx)
	}
}

// TestPlayback_CancelDiscardsBuffer ensures Ctrl+C does not silently
// keep the half-buffered reply around so the next agentDone (or a
// late tick from any future re-introduced loop) doesn't dump
// abandoned work into a fresh prompt.
func TestPlayback_CancelDiscardsBuffer(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.playbackBuf = []rune("buffered content")
	m.running = true

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)

	if len(m.playbackBuf) != 0 {
		t.Errorf("Ctrl+C must discard playback buffer; got len=%d", len(m.playbackBuf))
	}
}

// TestPlayback_ClearDiscardsBuffer is the /clear and Ctrl+L counterpart.
func TestPlayback_ClearDiscardsBuffer(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.playbackBuf = []rune("buffered content")

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "clear"})

	if len(m.playbackBuf) != 0 {
		t.Errorf("/clear must discard playback buffer; got len=%d", len(m.playbackBuf))
	}
}
