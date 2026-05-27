package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAgentStream_PumpsEveryEvent is the regression test for the bug
// where `/ask` (or any TUI question) showed the thinking timer tick
// but produced zero output text. Root cause: runAgent returned a
// tea.Cmd that read exactly ONE event from the agent goroutine's
// channel and then stopped. Tokens 2..N piled up in the buffered
// channel and the operator saw silence.
//
// This test injects an AgentRunner that emits a multi-token reply
// plus the terminal Done event, then drives the bubbletea Update
// loop the same way the real TUI does until the Done is processed.
// All emitted tokens must land in the stream's content buffer; if the
// pump regresses, only the first token shows up.
func TestAgentStream_PumpsEveryEvent(t *testing.T) {
	tokens := []string{"안", "녕", "하", "세", "요", "!"}

	deps := makeDeps()
	deps.Model = "claude-test"
	// Fake provider so the setup-gate (m.deps.Model != "") opens. The
	// runner stub never calls the provider — it just emits the events
	// it was told to.
	deps.SwapModel = func(string) error { return nil }
	deps.AgentRunner = func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
		for _, tok := range tokens {
			emit(AgentEvent{Token: tok})
		}
		// runAgent's goroutine appends its own {Done: true} after this
		// returns, so we don't emit it here.
	}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Kick off an agent run as if the operator pressed Enter on "ask".
	next, cmd := m.Update(submitMsg("ask"))
	m = next.(Model)

	// Drain the bubbletea Cmd graph. We maintain a FIFO of pending
	// cmds; each one is invoked exactly like real bubbletea would,
	// the returned msg is fed to Update, and any new cmd is appended.
	// tea.Batch unwraps into its constituent cmds. Stops when no
	// more pending cmds (clean exit) or when agentDoneMsg has been
	// processed (the streaming run is complete).
	pending := []tea.Cmd{cmd}
	gotDone := false
	deadline := time.Now().Add(5 * time.Second)
	for len(pending) > 0 && !gotDone {
		if time.Now().After(deadline) {
			t.Fatalf("pump loop did not terminate within 5s — likely deadlocked")
		}
		c := pending[0]
		pending = pending[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		// tea.Batch returns a BatchMsg ([]tea.Cmd). Unwrap so each
		// constituent runs independently.
		if batch, ok := msg.(tea.BatchMsg); ok {
			pending = append(pending, batch...)
			continue
		}
		// Drop the thinking-row animation tick so it does not re-arm
		// itself and burn 250ms per iteration. The test cares about
		// the agent stream pump, not the cosmetic "✦ Thinking…" timer.
		if _, isThinking := msg.(thinkingTickMsg); isThinking {
			continue
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		if follow != nil {
			pending = append(pending, follow)
		}
		if _, done := msg.(agentDoneMsg); done {
			gotDone = true
		}
	}
	// Drain any flush + playback ticks that arrived after the Done
	// event so the final batch of tokens lands in content before the
	// assertion. The playback drain matters because sentence-atomic
	// emission only flushes a trailing partial (e.g. "안녕하세요!"
	// without a following space) once running == false — that happens
	// on the next playbackTickMsg after agentDoneMsg.
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			pending = append(pending, batch...)
			continue
		}
		switch msg.(type) {
		case streamFlushTickMsg, playbackTickMsg:
		default:
			continue
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		if follow != nil {
			pending = append(pending, follow)
		}
	}
	if !gotDone {
		t.Fatal("agentDoneMsg never arrived — pump loop terminated early")
	}

	got := m.stream.content.String()
	want := strings.Join(tokens, "")
	if !strings.Contains(got, want) {
		t.Errorf("stream missing tokens.\n want substring: %q\n full content:   %q\n"+
			"  hint: if only the first token is present, the pump-cmd regression is back",
			want, got)
	}
}

// TestAgentStream_ClearsAgentChOnDone confirms that agentDoneMsg
// releases the channel field. Without this clear, a subsequent
// runAgent call could race against a leftover pump-cmd referencing
// the old (possibly closed) channel.
func TestAgentStream_ClearsAgentChOnDone(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Hand-install a fake agentCh so we can observe it being cleared.
	ch := make(chan agentEventMsg, 1)
	close(ch)
	m.agentCh = ch

	next, _ = m.Update(agentDoneMsg{})
	m = next.(Model)

	if m.agentCh != nil {
		t.Error("agentDoneMsg must clear m.agentCh to avoid stale-channel reads on the next run")
	}
	if m.running {
		t.Error("agentDoneMsg must clear m.running")
	}
}
