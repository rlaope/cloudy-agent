package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/render"
)

// Run builds the TUI Model, wires the agent runner, and starts the bubbletea
// program with the alternate screen. It blocks until the user quits.
func Run(ctx context.Context, deps Deps) error {
	if deps.Provider == nil {
		return fmt.Errorf("no LLM provider configured — run `cloudy setup` first")
	}

	provider, ok := deps.Provider.(llm.Provider)
	if !ok {
		return fmt.Errorf("invalid provider type")
	}

	// Wire the agent runner into Deps.
	deps.AgentRunner = makeAgentRunner(ctx, provider, deps)

	m := NewModel(deps)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// makeAgentRunner returns an AgentRunner closure that creates a real agent.Agent
// and bridges its render.Stream output into AgentEvent callbacks.
func makeAgentRunner(rootCtx context.Context, provider llm.Provider, deps Deps) func(cancelCh interface{}, input string, emit func(AgentEvent)) {
	// conversation history accumulates across turns
	var history []llm.Message

	return func(cancelChIface interface{}, input string, emit func(AgentEvent)) {
		cancelCh, _ := cancelChIface.(<-chan struct{})

		// Build a context that is cancelled when cancelCh is closed.
		runCtx, cancel := context.WithCancel(rootCtx)
		defer cancel()
		if cancelCh != nil {
			go func() {
				select {
				case <-cancelCh:
					cancel()
				case <-runCtx.Done():
				}
			}()
		}

		// Determine active skill.
		var activeSkill interface{ GetSkill() interface{} }
		_ = activeSkill

		opts := agent.Options{
			Provider: provider,
			Model:    deps.Model,
			Registry: deps.Tools,
			History:  history,
		}

		ag, err := agent.New(opts)
		if err != nil {
			emit(AgentEvent{Err: err, Done: true})
			return
		}

		// Build a render.Stream backed by an eventWriter that converts writes
		// into AgentEvent token emissions.
		ew := &eventWriter{emit: emit}
		theme := render.NewTheme(false)
		stream := render.NewStream(ew, theme)

		// We need to intercept BeginToolCall / EndToolCall — the render.Stream
		// writes formatted text, so we wrap the underlying writer to detect the
		// markers. For v1 we instead use a custom stream wrapper.
		interceptStream := newInterceptStream(emit, theme)

		// Wire usage callback: emit AgentEvent with Usage set.
		interceptStream.OnUsage = func(u llm.Usage) {
			emit(AgentEvent{Usage: &agentUsageMsg{
				Input:  u.InputTokens,
				Output: u.OutputTokens,
				USD:    u.CostUSD,
			}})
		}

		newMsgs, runErr := ag.Run(runCtx, input, interceptStream)
		_ = stream // kept for future use

		// Accumulate history for multi-turn.
		if len(newMsgs) > 0 {
			history = newMsgs
		}

		emit(AgentEvent{Done: true, Err: runErr})
	}
}

// eventWriter implements io.Writer by breaking writes into token AgentEvents.
type eventWriter struct {
	emit func(AgentEvent)
}

func (w *eventWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.emit(AgentEvent{Token: string(p)})
	}
	return len(p), nil
}

// interceptStream wraps render.Stream to intercept tool call boundaries and
// emit structured AgentEvents for the TUI stream component.
type interceptStream struct {
	*render.Stream
	emit  func(AgentEvent)
	inner *interceptWriter
}

type interceptWriter struct {
	emit    func(AgentEvent)
	inTool  bool
	toolBuf strings.Builder
}

func (w *interceptWriter) Write(p []byte) (int, error) {
	// All writes from render.Stream come through here; we emit as tokens.
	text := string(p)
	w.emit(AgentEvent{Token: text})
	return len(p), nil
}

func (w *interceptWriter) Flush() error { return nil }

func newInterceptStream(emit func(AgentEvent), theme render.Theme) *render.Stream {
	iw := &interceptWriter{emit: emit}
	return render.NewStream(iw, theme)
}

// writeHelp writes the help text to w for use in non-TUI contexts.
func writeHelp(w io.Writer) {
	fmt.Fprint(w, helpText())
}
