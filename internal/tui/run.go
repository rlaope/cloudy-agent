package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/wiring"
)

// providerRef is a mutex-guarded holder for the active LLM provider and
// model id. The agent runner reads through it on every turn so that a
// /login or /model swap takes effect on the next user question without
// rebuilding the runner closure or restarting the TUI.
type providerRef struct {
	mu       sync.RWMutex
	provider llm.Provider
	model    string
}

func (r *providerRef) get() (llm.Provider, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.provider, r.model
}

func (r *providerRef) set(p llm.Provider, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provider = p
	r.model = model
}

// Run builds the TUI Model, wires the agent runner, and starts the bubbletea
// program with the alternate screen. It blocks until the user quits.
// When deps.Provider is nil (no config yet) the TUI still opens so the user
// can run /setup or /login from inside.
func Run(ctx context.Context, deps Deps) error {
	ref := &providerRef{provider: deps.Provider, model: deps.Model}
	deps.AgentRunner = makeAgentRunner(ctx, ref, deps)
	deps.SwapModel = makeSwapModel(ref, deps.Model)

	m := NewModel(deps)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// makeSwapModel returns a SwapModel closure that resolves modelID to a
// fresh provider via wiring.BuildProvider, atomically swaps it into the
// shared providerRef the agent runner reads from, and persists the new
// DefaultModel into cloudy.yaml so the next launch picks it up.
//
// On any error (unknown model, missing env var, save failure) the ref
// is left untouched and the error is returned to the caller for
// inline reporting; the previous provider stays active.
func makeSwapModel(ref *providerRef, initialModel string) func(string) error {
	return func(modelID string) error {
		if modelID == "" {
			return errors.New("swap: model id is empty")
		}
		provider, resolvedID, err := wiring.BuildProvider(modelID)
		if err != nil {
			return err
		}
		ref.set(provider, resolvedID)

		cfgPath := config.Path()
		cfg, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			// Treat a missing/corrupt cloudy.yaml as "start from default"
			// so a brand-new user can /login before /setup. The runtime
			// swap above has already taken effect; we just lose
			// persistence on this one error path.
			cfg = config.Default()
		}
		cfg.DefaultModel = resolvedID
		if err := config.Save(cfgPath, cfg); err != nil {
			return fmt.Errorf("swap: persist DefaultModel: %w", err)
		}
		return nil
	}
}

// makeAgentRunner returns an AgentRunner that creates a real agent.Agent and
// bridges its render.Sink output into AgentEvent callbacks. The returned
// closure preserves conversation history across turns within one session.
//
// The provider+model are read through ref on every invocation, so a /login
// or /model swap performed mid-session is picked up on the very next turn
// without rebuilding the runner.
func makeAgentRunner(rootCtx context.Context, ref *providerRef, deps Deps) func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
	var history []llm.Message

	return func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
		provider, modelID := ref.get()
		if provider == nil || modelID == "" {
			emit(AgentEvent{
				Done: true,
				Err:  errors.New("no LLM provider configured; run /login to add an API key"),
			})
			return
		}

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

		approver := func(ctx context.Context, call llm.ToolCall) (bool, error) {
			reply := make(chan bool, 1)
			emit(AgentEvent{Approval: &ApprovalRequest{
				Tool:  call.Name,
				Args:  string(call.Arguments),
				Reply: reply,
			}})
			select {
			case ok := <-reply:
				return ok, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}

		ag, err := agent.New(agent.Options{
			Provider: provider,
			Model:    modelID,
			RegistryFn: func() *tools.Registry {
				if r := wiring.Current(); r != nil {
					return r
				}
				return deps.Tools
			},
			History:                  history,
			MaxTokensPerSession:      deps.MaxTokensPerSession,
			MaxUSDPerDay:             deps.MaxUSDPerDay,
			MaxConversationSeconds:   deps.MaxConversationSeconds,
			MaxLogLinesPerCall:       deps.MaxLogLinesPerCall,
			MaxProfileSecondsPerCall: deps.MaxProfileSecondsPerCall,
			MaxLogResponseBytes:      deps.MaxLogResponseBytes,
			Approver:                 approver,
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
