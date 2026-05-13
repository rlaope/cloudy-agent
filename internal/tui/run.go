package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/llm"
)

// Run builds the TUI Model, wires the agent runner, and starts the bubbletea
// program with the alternate screen. It blocks until the user quits.
func Run(ctx context.Context, deps Deps) error {
	if deps.Provider == nil {
		return fmt.Errorf("no LLM provider configured — run `cloudy setup` first")
	}

	deps.AgentRunner = makeAgentRunner(ctx, deps.Provider, deps)

	m := NewModel(deps)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// makeAgentRunner returns an AgentRunner that creates a real agent.Agent and
// bridges its render.Sink output into AgentEvent callbacks. The returned
// closure preserves conversation history across turns within one session.
func makeAgentRunner(rootCtx context.Context, provider llm.Provider, deps Deps) func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
	var history []llm.Message

	return func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
		runCtx, cancelCtx := context.WithCancel(rootCtx)
		defer cancelCtx()
		if cancel != nil {
			go func() {
				select {
				case <-cancel:
					cancelCtx()
				case <-runCtx.Done():
				}
			}()
		}

		ag, err := agent.New(agent.Options{
			Provider: provider,
			Model:    deps.Model,
			Registry: deps.Tools,
			History:  history,
		})
		if err != nil {
			emit(AgentEvent{Err: err, Done: true})
			return
		}

		newMsgs, runErr := ag.Run(runCtx, input, &tuiSink{emit: emit})
		if len(newMsgs) > 0 {
			history = newMsgs
		}
		emit(AgentEvent{Done: true, Err: runErr})
	}
}

// tuiSink translates render.Sink callbacks into AgentEvent values that the
// bubbletea Update loop already understands. The previous indirection
// (write-bytes-then-parse-the-markers-back-out) is gone — tool boundaries
// arrive as structured events directly.
type tuiSink struct {
	emit func(AgentEvent)
}

func (s *tuiSink) WriteToken(tok string) { s.emit(AgentEvent{Token: tok}) }

func (s *tuiSink) BeginToolCall(name, args string) {
	s.emit(AgentEvent{ToolBegin: &toolBeginEvt{name: name, args: args}})
}

func (s *tuiSink) EndToolCall(observation string, err error) {
	s.emit(AgentEvent{ToolEnd: &toolEndEvt{observation: observation, err: err}})
}

func (s *tuiSink) RecordUsage(u llm.Usage) {
	s.emit(AgentEvent{Usage: &agentUsageMsg{
		Input:  u.InputTokens,
		Output: u.OutputTokens,
		USD:    u.CostUSD,
	}})
}
