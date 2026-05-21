package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPlayback_BuffersTokensRatherThanStreaming pins the core contract:
// an LLM token does NOT land in the stream content immediately. It is
// accumulated in playbackBuf and drained at a fixed cadence by the
// playback tick so the visible output flows like typing rather than
// mirroring the SSE burst pattern.
func TestPlayback_BuffersTokensRatherThanStreaming(t *testing.T) {
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

	// Pump the very first agent event so the runner's emit lands.
	if m.agentCh == nil {
		t.Fatal("submitMsg should install agentCh")
	}
	msg := <-m.agentCh
	next, _ = m.Update(agentEventMsg(msg))
	m = next.(Model)

	// The styled bullet prefix is emitted synchronously (atomic ANSI),
	// so stream.content carries it immediately. The prose runes are
	// still queued for playback and have NOT landed yet.
	if !strings.Contains(m.stream.content.String(), "●") {
		t.Errorf("first token must emit the ● bullet prefix synchronously; content: %q",
			m.stream.content.String())
	}
	if strings.Contains(m.stream.content.String(), "안녕") {
		t.Errorf("prose runes must wait for the playback tick, not land immediately; content: %q",
			m.stream.content.String())
	}
	if len(m.playbackBuf) == 0 {
		t.Error("prose runes must be queued in playbackBuf")
	}
	if !m.playbackActive {
		t.Error("playbackActive should be true so a tick is scheduled")
	}
}

// TestPlayback_TickDrainsBoundedRunes confirms the playback handler
// pops at most playbackRunesPerTick runes per tick, then re-arms only
// when more remain. Without the bound a single tick could dump the
// whole buffer and defeat the typewriter pacing.
func TestPlayback_TickDrainsBoundedRunes(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Seed the buffer with more runes than fit in a single tick.
	long := []rune("abcdefghij") // 10 runes, 2x the per-tick cap
	m.playbackBuf = append(m.playbackBuf, long...)
	m.playbackActive = true

	next, cmd := m.Update(playbackTickMsg{})
	m = next.(Model)

	got := m.stream.content.String()
	if got == "" {
		t.Fatal("first tick must emit at least one rune")
	}
	if len([]rune(got)) > playbackRunesPerTick {
		t.Errorf("single tick emitted %d runes; cap is %d", len([]rune(got)), playbackRunesPerTick)
	}
	if len(m.playbackBuf) != len(long)-playbackRunesPerTick {
		t.Errorf("buffer should shrink by exactly playbackRunesPerTick; got len=%d want=%d",
			len(m.playbackBuf), len(long)-playbackRunesPerTick)
	}
	if cmd == nil {
		t.Error("tick with leftover buffer must re-arm; got nil cmd")
	}
}

// TestPlayback_TickRespectsMultiByteRunes is the regression that
// motivated using []rune instead of []byte for playbackBuf: a slice
// cut at a fixed byte offset could split a Korean character and emit
// invalid UTF-8 to the terminal. Pop the cap as runes; each rune
// must survive intact.
func TestPlayback_TickRespectsMultiByteRunes(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	korean := []rune("가나다라마바사아자차") // 10 runes, 30 bytes
	m.playbackBuf = append(m.playbackBuf, korean...)
	m.playbackActive = true

	next, _ = m.Update(playbackTickMsg{})
	m = next.(Model)

	out := m.stream.content.String()
	for _, r := range out {
		if r == '�' {
			t.Fatalf("playback emitted a Unicode replacement char — split a Korean rune; got: %q", out)
		}
	}
}

// TestPlayback_NewlineGetsContinuationIndent pins the indent contract.
// A token containing "\n" must be followed by assistantContIndent so
// wrapped paragraphs stay aligned under the bullet rather than
// flushing to column 0.
func TestPlayback_NewlineGetsContinuationIndent(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.bufferAssistantToken("first\nsecond")
	got := string(m.playbackBuf)
	want := "first\n" + assistantContIndent + "second"
	if got != want {
		t.Errorf("newline indent transform wrong\n got:  %q\n want: %q", got, want)
	}
}

// TestPlayback_ToolBeginForcesImmediateDrain is the contract that
// stops a tool block from leapfrogging the prose that introduced it.
// When ToolBegin arrives mid-playback, the buffered prose must flush
// synchronously before the tool header lands.
func TestPlayback_ToolBeginForcesImmediateDrain(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.assistantTurnStarted = true
	m.bufferAssistantToken("checking pods now…")
	m.playbackActive = true
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
	// Tool header should follow the prose, not precede it.
	proseIdx := strings.Index(content, "checking pods now")
	toolIdx := strings.Index(content, "k8s.get_pods")
	if proseIdx == -1 || toolIdx == -1 {
		t.Fatalf("both prose and tool header should be present; content: %q", content)
	}
	if proseIdx >= toolIdx {
		t.Errorf("prose must appear before tool header; prose=%d tool=%d", proseIdx, toolIdx)
	}
}

// TestPlayback_CancelDiscardsBuffer ensures Ctrl+C / Esc do not
// silently keep typing a half-played reply the operator just
// abandoned.
func TestPlayback_CancelDiscardsBuffer(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.playbackBuf = []rune("buffered content")
	m.playbackActive = true
	m.running = true

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)

	if len(m.playbackBuf) != 0 {
		t.Errorf("Ctrl+C must discard playback buffer; got len=%d", len(m.playbackBuf))
	}
	if m.playbackActive {
		t.Error("Ctrl+C must clear playbackActive")
	}
}

// TestPlayback_ClearDiscardsBuffer is the /clear and Ctrl+L counterpart.
func TestPlayback_ClearDiscardsBuffer(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.playbackBuf = []rune("buffered content")
	m.playbackActive = true

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "clear"})

	if len(m.playbackBuf) != 0 {
		t.Errorf("/clear must discard playback buffer; got len=%d", len(m.playbackBuf))
	}
	if m.playbackActive {
		t.Error("/clear must clear playbackActive")
	}
}
