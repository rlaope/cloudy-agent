package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
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
	// WithMouseCellMotion captures wheel events as tea.MouseMsg so the
	// stream viewport can scroll. Without it the alt-screen terminal
	// emulator translates wheel into ↑/↓ key sequences, which the
	// prompt textarea interprets as history navigation — the opposite
	// of what the operator means when they scroll up to read history.
	//
	// Mouse capture also intercepts drag, which disables the terminal's
	// native click-and-drag text selection — annoying when the operator
	// wants to copy a chunk of output. CLOUDY_NO_MOUSE=1 opts out so
	// drag-to-select works at the cost of in-app wheel scrolling (use
	// PgUp/PgDn or arrow keys instead).
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if !mouseCaptureDisabled() {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)
	_, err := p.Run()
	return err
}

// mouseCaptureDisabled reports whether the operator has opted out of bubble
// tea's mouse capture via CLOUDY_NO_MOUSE. Any non-empty value other than
// the literal "0" / "false" counts as opt-out so common shapes (`=1`,
// `=true`, `=yes`) all work without a parser.
func mouseCaptureDisabled() bool {
	v := os.Getenv("CLOUDY_NO_MOUSE")
	switch v {
	case "", "0", "false", "FALSE", "False":
		return false
	}
	return true
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

		// Re-load the active permission profile each turn so a mid-
		// session `cloudy profile use foo` swap is honoured by the next
		// agent run without restarting the TUI. The hot path is fast —
		// it is a single small YAML read from ~/.cloudy/profiles/.
		activeProfile, _ := permission.LoadActive()

		ag, err := agent.New(agent.Options{
			Provider: provider,
			Model:    modelID,
			RegistryFn: func() *tools.Registry {
				if r := wiring.Current(); r != nil {
					return r
				}
				return deps.Tools
			},
			Skills:                   deps.Skills,
			History:                  history,
			MaxTokensPerSession:      deps.MaxTokensPerSession,
			MaxUSDPerDay:             deps.MaxUSDPerDay,
			MaxConversationSeconds:   deps.MaxConversationSeconds,
			MaxLogLinesPerCall:       deps.MaxLogLinesPerCall,
			MaxProfileSecondsPerCall: deps.MaxProfileSecondsPerCall,
			MaxLogResponseBytes:      deps.MaxLogResponseBytes,
			Approver:                 approver,
			Profile:                  activeProfile,
		})
		if err != nil {
			logSessionError(deps.Session, "agent.new", err)
			emit(AgentEvent{Err: err, Done: true})
			return
		}

		newMsgs, runErr := ag.Run(runCtx, input, &tuiSink{emit: emit, sess: deps.Session, modelID: modelID})
		if len(newMsgs) > 0 {
			history = newMsgs
		}
		if runErr != nil {
			logSessionError(deps.Session, "agent.run", runErr)
		}
		emit(AgentEvent{Done: true, Err: runErr})
	}
}

// tuiSink translates render.Sink callbacks into AgentEvent values that the
// bubbletea Update loop already understands. The previous indirection
// (write-bytes-then-parse-the-markers-back-out) is gone — tool boundaries
// arrive as structured events directly.
//
// When sess is non-nil, the sink ALSO mirrors a narrow, safe subset of events
// to the on-disk session log so a post-mortem can identify which tools ran
// and which errored. Deliberately NOT mirrored (deferred until the
// MaskingHook pipeline is reachable from this seam):
//
//   - tool arguments — may contain credentials/PII (e.g. db.query connection
//     strings, http.api bearer tokens) and are not currently masked here.
//   - tool observations / result text — masked by AfterToolCall hooks at the
//     agent layer; the sink sees the pre-mask bytes, so writing them would
//     re-open the v0.5 M-1 redaction gap on disk.
//   - assistant prose / WriteToken streams — would balloon the JSONL and we
//     have no end-of-turn boundary for the assistant text yet.
//
// modelID is captured so KindUsage events carry it via Event.Name, letting
// `cloudy session list` populate the Model column from readMeta.
//
// lastTool stores the most recent tool name seen by BeginToolCall so the
// matching EndToolCall error path can attribute the failure to a real tool
// (not the placeholder "tool"). Today the agent iterates tool calls
// sequentially (internal/agent/agent.go:363 — single goroutine, single loop),
// so a scalar is sufficient; when parallel tool dispatch lands this needs to
// move to a per-call-id map.
type tuiSink struct {
	emit     func(AgentEvent)
	sess     *session.Session
	modelID  string
	lastTool string
}

func (s *tuiSink) WriteToken(tok string) { s.emit(AgentEvent{Token: tok}) }

func (s *tuiSink) BeginToolCall(name, args string) {
	s.emit(AgentEvent{ToolBegin: &toolBeginEvt{name: name, args: args}})
	if s.sess != nil {
		s.lastTool = name
		_ = s.sess.Append(session.Event{
			Kind: session.KindToolCall,
			Name: name,
		})
	}
}

func (s *tuiSink) EndToolCall(observation string, err error) {
	s.emit(AgentEvent{ToolEnd: &toolEndEvt{observation: observation, err: err}})
	if s.sess == nil || err == nil {
		// Success-path observations are intentionally not persisted — see
		// the type doc on tuiSink. The corresponding KindToolCall (with the
		// tool name) is already on disk, which is enough to see the agent's
		// trajectory; the rich payload waits on the masker wiring.
		return
	}
	name := s.lastTool
	if name == "" {
		name = "tool"
	}
	logSessionError(s.sess, name, err)
}

func (s *tuiSink) RecordUsage(u llm.Usage) {
	s.emit(AgentEvent{Usage: &agentUsageMsg{
		Input:  u.InputTokens,
		Output: u.OutputTokens,
		USD:    u.CostUSD,
	}})
	if s.sess != nil {
		_ = s.sess.Append(session.Event{
			Kind: session.KindUsage,
			Name: s.modelID,
			Tokens: &session.Tokens{
				Input:  u.InputTokens,
				Output: u.OutputTokens,
				USD:    u.CostUSD,
			},
		})
	}
}

// logSessionError appends a KindError event to sess when sess and err are
// both non-nil and err is not a plain context cancellation (skipping ctx
// cancels keeps the log signal-heavy when the operator just hit Ctrl+C).
func logSessionError(sess *session.Session, name string, err error) {
	if sess == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	_ = sess.Append(session.Event{
		Kind: session.KindError,
		Name: name,
		Text: err.Error(),
	})
}
