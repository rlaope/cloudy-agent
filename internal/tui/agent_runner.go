package tui

// agent_runner.go owns the bubbletea-side controller for an in-flight agent
// turn: the discriminated-union event type the runner emits, the message
// envelopes those events ride on, and the Model methods that pump them
// (runAgent → pumpAgentCmd → applyAgentEvent). The actual agent-goroutine
// builder lives in run.go (makeAgentRunner); this file is the consumer
// side that lives on the tea.Model. Split out of app.go so the root file
// stays focused on the Update dispatch + View composition.

import (
	tea "github.com/charmbracelet/bubbletea"
)

// AgentEvent is a discriminated union of events emitted by the agent runner.
type AgentEvent struct {
	Token     string // non-empty → text delta
	ToolBegin *toolBeginEvt
	ToolEnd   *toolEndEvt
	Done      bool
	Err       error
	Cost      float64
	// Usage is non-nil when the agent emits token-usage data.
	Usage *agentUsageMsg
	// ScopeAddendum is prepended to the system prompt when non-empty.
	ScopeAddendum string
	// Approval is non-nil when the agent has paused on a RiskHigh tool and
	// is awaiting an explicit y/n decision from the operator. The TUI sends
	// the decision back via Reply; the agent goroutine blocks until then.
	Approval *ApprovalRequest
}

// ApprovalRequest is the payload of an AgentEvent that asks the operator to
// authorise a single high-risk tool call. The agent goroutine is blocked on
// Reply until the TUI receives a y/n keypress and sends the answer.
type ApprovalRequest struct {
	Tool  string
	Args  string
	Reply chan<- bool
}

type toolBeginEvt struct{ name, args string }
type toolEndEvt struct {
	observation string
	err         error
}

// agentDoneMsg is sent when the agent finishes a run.
type agentDoneMsg struct{ err error }

// agentEventMsg delivers a streaming event from the agent goroutine.
type agentEventMsg AgentEvent

// agentUsageMsg carries cumulative token usage from a streaming LLM response.
type agentUsageMsg struct {
	Input  int
	Output int
	USD    float64
}

// cancelMsg signals that the in-flight agent run should be cancelled.
type cancelMsg struct{}

// usageAccum accumulates token usage across turns.
type usageAccum struct {
	Input  int
	Output int
	USD    float64
}

// runAgent starts the agent in a goroutine and returns a tea.Cmd that delivers
// events back to the program via agentEventMsg / agentDoneMsg.
func (m *Model) runAgent(input string) tea.Cmd {
	if m.deps.AgentRunner == nil {
		return func() tea.Msg {
			return agentDoneMsg{err: nil}
		}
	}

	// Prepend scope addendum so the agent honors the session scope.
	if addendum := m.scope.SystemPromptAddendum(); addendum != "" {
		input = addendum + "\n\n" + input
	}

	// Create a cancellable context using a simple channel-based approach.
	done := make(chan struct{})
	var cancelled bool
	oldCancel := m.cancel
	oldCancel() // cancel previous run if any

	m.running = true
	m.cancel = func() {
		if !cancelled {
			cancelled = true
			close(done)
		}
	}

	ch := make(chan agentEventMsg, 64)
	m.agentCh = ch

	go func() {
		defer close(ch)
		m.deps.AgentRunner(done, input, func(evt AgentEvent) {
			select {
			case ch <- agentEventMsg(evt):
			case <-done:
			}
		})
		ch <- agentEventMsg{Done: true}
	}()

	return m.pumpAgentCmd()
}

// pumpAgentCmd returns a tea.Cmd that reads ONE event from m.agentCh
// and dispatches it as the next tea.Msg. Bubbletea Cmds fire once, so
// to drain the agent goroutine's stream we re-issue this command after
// every applied event (see applyAgentEvent). The Done sentinel converts
// into agentDoneMsg so the parent flow can finalise without a separate
// channel-closed check.
func (m *Model) pumpAgentCmd() tea.Cmd {
	ch := m.agentCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return agentDoneMsg{}
		}
		if AgentEvent(evt).Done {
			return agentDoneMsg{err: AgentEvent(evt).Err}
		}
		return evt
	}
}

// flushPendingUserEcho returns and clears the queued user-echo chip.
// Returns "" when no chip is pending so callers can string-concat the
// result unconditionally. Called from the first applyAgentEvent path
// (so the chip moves into the stream the instant the reply starts)
// plus the cancel/error/no-model paths so the operator's question is
// never silently lost from the transcript.
func (m *Model) flushPendingUserEcho() string {
	if m.pendingUserEcho == "" {
		return ""
	}
	out := m.pendingUserEcho
	m.pendingUserEcho = ""
	return out
}

// applyAgentEvent routes a single agent event to the appropriate sub-model.
func (m *Model) applyAgentEvent(evt AgentEvent) tea.Cmd {
	var cmds []tea.Cmd

	// First event of a turn: the queued user chip has been sitting
	// above the prompt; drain it into the stream now so it scrolls
	// into history alongside the reply that is about to land.
	if flush := m.flushPendingUserEcho(); flush != "" {
		var fCmd tea.Cmd
		m.stream, fCmd = m.stream.Update(streamWriteMsg(flush))
		cmds = append(cmds, fCmd)
	}

	if evt.Token != "" {
		// Switch the thinking row from verb-cycling to "Typing" once
		// real bytes have entered the playback pipeline. tokens is a
		// coarse char-rate stand-in until evt.Usage from the provider
		// gives us a true token count.
		m.thinking.streaming = true
		m.thinking.tokens += approxTokens(evt.Token)
		// First token of a turn: emit the styled "●  " bullet via
		// the synchronous chrome path so the lipgloss-generated ANSI
		// escape sequences land atomically. If we let those sequences
		// flow through the rune-by-rune playback tick they could
		// split mid-escape ("\x1b[3" + "8;5;1" + …), breaking the
		// terminal's render of every byte that followed.
		if !m.assistantTurnStarted {
			m.assistantTurnStarted = true
			prefix := "\n" + assistantPrefixStyle.Render("●") + " "
			var prefixCmd tea.Cmd
			m.stream, prefixCmd = m.stream.Update(streamWriteMsg(prefix))
			cmds = append(cmds, prefixCmd)
		}
		// Buffer the actual prose runes for typewriter playback. The
		// playback tick drains the buffer at a steady ~125 chars/s so
		// the visible output flows like typing rather than mirroring
		// the upstream SSE burst pattern.
		m.bufferAssistantToken(evt.Token)
		if !m.playbackActive && len(m.playbackBuf) > 0 {
			m.playbackActive = true
			cmds = append(cmds, playbackTickCmd())
		}
	}
	if evt.ToolBegin != nil {
		// Flush any buffered assistant text *now* so the tool block
		// does not visually leapfrog the prose that introduced it.
		// We use streamWriteMsg (synchronous, immediate) rather than
		// the typewriter so the tool can dispatch without waiting on
		// the playback pace.
		if drain := m.drainPlaybackBuffer(); drain != "" {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamWriteMsg(drain))
			cmds = append(cmds, sCmd)
		}
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamToolBeginMsg{
			name: evt.ToolBegin.name,
			args: evt.ToolBegin.args,
		})
		cmds = append(cmds, sCmd)
	}
	if evt.ToolEnd != nil {
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamToolEndMsg{
			observation: evt.ToolEnd.observation,
			err:         evt.ToolEnd.err,
		})
		cmds = append(cmds, sCmd)
	}
	if evt.Cost > 0 {
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{cost: evt.Cost})
		cmds = append(cmds, hCmd)
	}
	if evt.Usage != nil {
		m.usage.Input += evt.Usage.Input
		m.usage.Output += evt.Usage.Output
		m.usage.USD += evt.Usage.USD
		// Prefer the provider's authoritative output-token count over the
		// approxTokens estimator built up from streaming chunks.
		if evt.Usage.Output > 0 {
			m.thinking.tokens = evt.Usage.Output
		}
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{cost: evt.Usage.USD})
		cmds = append(cmds, hCmd)
	}
	if evt.Approval != nil {
		// A high-risk tool call has paused the agent. Any open overlay
		// (palette suggestions, arrow picker) must yield so the y/N
		// keystroke reaches the approval gate; otherwise the agent
		// goroutine stays blocked on Reply.
		m.dismissOpenOverlays()
		m.pendingApproval = evt.Approval
	}

	return tea.Batch(cmds...)
}
