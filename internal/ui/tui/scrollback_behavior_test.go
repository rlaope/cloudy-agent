package tui

import (
	"strings"
	"testing"
)

// drainPlayback drives playbackTickMsg until the buffer empties (or the
// guard trips), accumulating everything printed to scrollback.
func drainPlayback(t *testing.T, m Model) (Model, string) {
	t.Helper()
	var committed string
	for i := 0; i < 2000 && len(m.playbackBuf) > 0; i++ {
		next, cmd := m.Update(playbackTickMsg{})
		m = next.(Model)
		committed += printedText(cmd)
	}
	if len(m.playbackBuf) != 0 {
		t.Fatalf("playback never drained; left=%d", len(m.playbackBuf))
	}
	return m, committed
}

// TestNativeScrollback_CommitResetsViewport pins the core of the
// native-scrollback redesign: once a turn's reply has played back, it is
// printed to the terminal's real scrollback (tea.Println) and the live
// viewport collapses to empty so the next turn starts fresh below it.
func TestNativeScrollback_CommitResetsViewport(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.bufferAssistantToken("body text here.")
	next, _ = m.Update(agentDoneMsg{})
	m = next.(Model)

	m, committed := drainPlayback(t, m)

	if !m.stream.Empty() {
		t.Errorf("live viewport must be empty after the turn is committed to scrollback; got %q",
			m.stream.content.String())
	}
	if !strings.Contains(stripANSI(committed), "body text here.") {
		t.Errorf("turn body must be printed to native scrollback; got %q", stripANSI(committed))
	}
}

// TestFullscreen_KeepsTranscriptInViewport guards the alt-screen path:
// tea.Println is a no-op under the alt-screen, so fullscreen mode must
// accumulate the transcript in the scrollable viewport and never try to
// "commit" it away (which would silently drop it).
func TestFullscreen_KeepsTranscriptInViewport(t *testing.T) {
	m := NewModel(makeDeps())
	m.fullscreen = true
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.bufferAssistantToken("kept body.")
	next, _ = m.Update(agentDoneMsg{})
	m = next.(Model)

	m, committed := drainPlayback(t, m)

	if m.stream.Empty() {
		t.Error("fullscreen must keep the transcript in the in-app viewport, not commit it away")
	}
	if committed != "" {
		t.Errorf("fullscreen must not print to scrollback (tea.Println is a no-op in alt-screen); got %q", committed)
	}
}

// TestSubmitDuringPlayback_CommitsPreviousTurn pins the bug-#4 fix: a new
// question that arrives while the previous reply is still typing out must
// finalise that reply first — drain its buffer, commit it to scrollback,
// and collapse the viewport — so the old answer doesn't dump into and
// interleave with the new turn. We assert on model state (the commit +
// reset happen synchronously inside the submit Update) rather than
// executing the returned batch, which also kicks the agent runner.
func TestSubmitDuringPlayback_CommitsPreviousTurn(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.bufferAssistantToken("first answer.")
	next, _ = m.Update(agentDoneMsg{})
	m = next.(Model)
	if !m.playbackActive {
		t.Fatal("setup: agentDone should have armed playback")
	}

	// New question arrives mid-playback.
	next, _ = m.Update(submitMsg("second question"))
	m = next.(Model)

	if m.playbackActive {
		t.Error("a new submit must finalise the prior playback (playbackActive should be false)")
	}
	if len(m.playbackBuf) != 0 {
		t.Errorf("the prior playback buffer must be drained on new submit; left=%d", len(m.playbackBuf))
	}
	if !m.stream.Empty() {
		t.Errorf("the prior turn must be committed and the viewport reset before the new turn; got %q",
			m.stream.content.String())
	}
}
