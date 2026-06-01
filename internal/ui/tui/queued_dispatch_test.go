package tui

import "testing"

// TestSubmit_WhileRunning_QueuesFollowup pins the fix for the mid-stream
// truncation + memory-loss bug: a prompt submitted while a turn is in flight
// must be QUEUED, not dispatched as a second agent run. The old code called
// runAgent unconditionally, and runAgent cancels the previous run — which
// drained the half-filled playback buffer (truncating the reply on screen) and
// dropped the cancelled turn from conversation history (a cancelled ag.Run
// returns no messages). Reaching the queue branch returns before runAgent, so
// asserting the prompt landed in queuedInputs proves the restart never fired.
func TestSubmit_WhileRunning_QueuesFollowup(t *testing.T) {
	deps := makeDeps()
	deps.Model = "claude-test"
	// Returns immediately and never emits: the turn's Done is never pumped
	// back in (we don't feed agentDoneMsg), so m.running stays true and the
	// follow-up sees an in-flight turn.
	deps.AgentRunner = func(_ <-chan struct{}, _ string, _ func(AgentEvent)) {}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, _ = m.Update(submitMsg("first question"))
	m = next.(Model)
	if !m.running {
		t.Fatal("first submit (idle) should start an in-flight turn")
	}
	if len(m.queuedInputs) != 0 {
		t.Fatalf("first submit (idle) must dispatch, not queue; queue=%v", m.queuedInputs)
	}

	next, _ = m.Update(submitMsg("second question"))
	m = next.(Model)
	if len(m.queuedInputs) != 1 || m.queuedInputs[0] != "second question" {
		t.Fatalf("submit while in flight must queue the follow-up; queue=%v", m.queuedInputs)
	}
}

// TestSubmit_SlashCommandWhileRunning_NotQueued guards that slash commands stay
// live during a turn (so /cancel, /model, /compact act immediately) instead of
// being parked in the follow-up queue.
func TestSubmit_SlashCommandWhileRunning_NotQueued(t *testing.T) {
	deps := makeDeps()
	deps.Model = "claude-test"
	deps.AgentRunner = func(_ <-chan struct{}, _ string, _ func(AgentEvent)) {}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, _ = m.Update(submitMsg("first question"))
	m = next.(Model)

	next, _ = m.Update(submitMsg("/compact"))
	m = next.(Model)
	if len(m.queuedInputs) != 0 {
		t.Fatalf("a slash command must not be queued as a follow-up; queue=%v", m.queuedInputs)
	}
}

// TestDispatchQueuedInput_PopsOldestAndRuns verifies the drain side: each
// turn-completion point calls dispatchQueuedInput, which must pop the OLDEST
// queued prompt (FIFO order preserved) and start it, returning nil only when
// the queue is empty.
func TestDispatchQueuedInput_PopsOldestAndRuns(t *testing.T) {
	deps := makeDeps()
	deps.Model = "claude-test"
	deps.AgentRunner = func(_ <-chan struct{}, _ string, _ func(AgentEvent)) {}

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	m.queuedInputs = []string{"a", "b"}
	if cmd := m.dispatchQueuedInput(); cmd == nil {
		t.Fatal("non-empty queue should return a dispatch cmd")
	}
	if len(m.queuedInputs) != 1 || m.queuedInputs[0] != "b" {
		t.Fatalf("dispatch should pop the oldest (FIFO); remaining=%v", m.queuedInputs)
	}
	if !m.running {
		t.Fatal("dispatch should start the popped turn (running=true)")
	}

	m.queuedInputs = nil
	if cmd := m.dispatchQueuedInput(); cmd != nil {
		t.Fatal("empty queue should return nil cmd")
	}
}
