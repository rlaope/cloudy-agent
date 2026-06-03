package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/memory"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
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

// convoState is the shared, mutex-guarded conversation state the agent
// runner and the /compact, /new, and /resume closures all operate on. It
// replaces the bare `history` closure variable so those commands — wired
// onto the Model, which cannot reach a closure local — can mutate the same
// history the next turn replays. sess is here too so /new can roll a fresh
// session file that the per-turn tuiSink picks up.
type convoState struct {
	mu      sync.Mutex
	history []llm.Message
	sess    *session.Session
	// version bumps on every history mutation. /compact captures it before
	// its (slow) summarizer round-trip and refuses to overwrite if it
	// changed underneath — so a concurrent agent turn, /new, or /resume is
	// never silently clobbered by a stale compaction result.
	version uint64
	// plan toggles plan-first investigation (agent.Options.Plan). On by
	// default in the TUI; the /plan command flips it. Read under mu when a
	// turn builds its agent so a toggle never races a running turn's snapshot.
	plan bool
}

// Run builds the TUI Model, wires the agent runner, and starts the bubbletea
// program with the alternate screen. It blocks until the user quits.
// When deps.Provider is nil (no config yet) the TUI still opens so the user
// can run /setup or /login from inside.
func Run(ctx context.Context, deps Deps) error {
	ref := &providerRef{provider: deps.Provider, model: deps.Model}
	state := &convoState{sess: deps.Session, plan: true}
	deps.AgentRunner = makeAgentRunner(ctx, ref, deps, state)
	deps.SwapModel = makeSwapModel(ref, deps.Model)
	deps.CompactHistory = makeCompactHistory(ref, state)
	deps.ResetHistory = makeResetHistory(state)
	deps.SeedHistory = makeSeedHistory(state)
	deps.TogglePlan = func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.plan = !state.plan
		return state.plan
	}

	m := NewModel(deps)
	m.fullscreen = fullscreenRequested()
	// Default = terminal-native mode: no alt-screen, no mouse capture.
	// This matches Claude Code's behaviour — assistant prose lives in
	// the main terminal buffer so the operator's native click-and-drag
	// text selection (and the terminal's copy shortcut) just works.
	//
	// The previous default (alt-screen + mouse capture) broke drag-to-
	// copy on iTerm2 / Terminal.app, which is the field complaint that
	// repeatedly came back ("Claude Code 되는데 cloudy 왜 안 됨").
	// Drag-to-select is a load-bearing UX expectation; we prioritise
	// it over the full-screen TUI feel.
	//
	// CLOUDY_FULLSCREEN=1 opts back into the full-screen mode for
	// operators who prefer the pinned header/footer and the in-app
	// wheel-scroll viewport, knowing the trade-off.
	opts := []tea.ProgramOption{}
	if fullscreenRequested() {
		opts = append(opts, tea.WithAltScreen(), tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)
	_, err := p.Run()
	return err
}

// fullscreenRequested reports whether CLOUDY_FULLSCREEN opts back into
// the alt-screen + mouse-capture TUI. Trimmed and case-insensitive so
// common shell shapes ("=1", "=true", "=yes", " on ") all enable.
func fullscreenRequested() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CLOUDY_FULLSCREEN")))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func loadSimilarIncidentCasesForTUI(input string, profile *permission.Profile) (string, error) {
	rendered, err := agent.BuildIncidentMemoryPrompt(input, profile)
	if err != nil {
		return "", err
	}
	return rendered, nil
}

// makeSwapModel returns a SwapModel closure that resolves modelID to a
// fresh provider via wiring.BuildProvider, atomically swaps it into the
// shared providerRef the agent runner reads from, and persists the new
// DefaultModel into config.yaml so the next launch picks it up.
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
			// Treat a missing/corrupt config.yaml as "start from default"
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
func makeAgentRunner(rootCtx context.Context, ref *providerRef, deps Deps, state *convoState) func(cancel <-chan struct{}, input string, emit func(AgentEvent)) {
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

		// Re-read durable cross-session memory each turn (a cheap markdown read)
		// so a memory.record made earlier this session is visible to the very
		// next turn without restarting — mirroring the per-turn profile reload.
		envMemory, _ := memory.Load()

		// Snapshot the shared conversation state. /compact, /new, and
		// /resume are gated on !running in the TUI, so sess/history are
		// stable for the duration of this turn; the lock just publishes
		// the reads safely.
		state.mu.Lock()
		history := state.history
		sess := state.sess
		planOn := state.plan
		state.mu.Unlock()

		similarCases, simErr := loadSimilarIncidentCasesForTUI(input, activeProfile)
		if simErr != nil {
			logSessionError(sess, "incidentmemory.retrieve", simErr)
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
			Plan:                     planOn,
			EnvironmentMemory:        envMemory,
			SimilarIncidentCases:     similarCases,
		})
		if err != nil {
			logSessionError(sess, "agent.new", err)
			emit(AgentEvent{Err: err, Done: true})
			return
		}

		// Redact tool args/observations before they touch the session log.
		// MaskerOrDefault never returns nil and falls back to the built-in
		// baseline when no profile is active, so the on-disk mirror is never
		// less redacted than the model-facing MaskingHook would produce.
		sinkMasker := permission.MaskerOrDefault(activeProfile)
		newMsgs, runErr := ag.Run(runCtx, input, &tuiSink{emit: emit, sess: sess, modelID: modelID, masker: sinkMasker})
		if len(newMsgs) > 0 {
			state.mu.Lock()
			state.history = newMsgs
			state.version++
			state.mu.Unlock()
			// Persist a masked resume snapshot so the conversation survives
			// a restart. MaskHistory is a hard requirement: the in-memory
			// history is not reliably masked (raw prompts/prose never are),
			// so we redact a deep copy before it touches disk.
			if sess != nil {
				masked := permission.MaskHistory(activeProfile, newMsgs)
				if saveErr := session.SaveHistory(sess.ID, modelID, masked); saveErr != nil {
					logSessionError(sess, "session.save", saveErr)
				}
			}
		}
		if runErr != nil {
			logSessionError(sess, "agent.run", runErr)
		}
		emit(AgentEvent{Done: true, Err: runErr})
	}
}

// tuiSink translates render.Sink callbacks into AgentEvent values that the
// bubbletea Update loop already understands. The previous indirection
// (write-bytes-then-parse-the-markers-back-out) is gone — tool boundaries
// arrive as structured events directly.
//
// When sess is non-nil, the sink mirrors tool activity to the on-disk session
// log so a post-mortem can replay which tools ran, with what arguments, and
// what they returned. Tool args (BeginToolCall) and observations (EndToolCall)
// are run through masker before they touch disk — masker is built via
// permission.MaskerOrDefault, so it is never nil and falls back to the
// built-in baseline when no profile is active. This closes the v0.5 M-1
// redaction gap that previously forced args/observations to be dropped because
// the AfterToolCall masker was unreachable from this seam.
//
// Still NOT mirrored:
//
//   - assistant prose / WriteToken streams — would balloon the JSONL and we
//     have no end-of-turn boundary for the assistant text yet.
//   - the raw user prompt — flows in as ag.Run's input, not through this sink.
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
	masker   *permission.Masker
	lastTool string
}

func (s *tuiSink) WriteToken(tok string) { s.emit(AgentEvent{Token: tok}) }

func (s *tuiSink) BeginToolCall(name, args string) {
	s.emit(AgentEvent{ToolBegin: &toolBeginEvt{name: name, args: args}})
	if s.sess != nil {
		s.lastTool = name
		ev := session.Event{Kind: session.KindToolCall, Name: name}
		// Tool arguments are JSON objects (NormalizeArguments guarantees it);
		// MaskJSON redacts key-named and value-pattern secrets, and no-ops on
		// non-JSON, so a connection string or bearer token never lands raw.
		if args != "" {
			if masked, err := s.masker.MaskJSON([]byte(args)); err == nil {
				ev.Args = masked
			}
		}
		_ = s.sess.Append(ev)
	}
}

func (s *tuiSink) EndToolCall(observation string, err error) {
	s.emit(AgentEvent{ToolEnd: &toolEndEvt{observation: observation, err: err}})
	if s.sess == nil {
		return
	}
	name := s.lastTool
	if name == "" {
		name = "tool"
	}
	if err != nil {
		logSessionError(s.sess, name, err)
		return
	}
	// Mirror the masked observation so a post-mortem can replay what each
	// tool returned. MaskString applies the value-pattern set; combined with
	// the masked args on the matching KindToolCall this gives a redacted but
	// faithful trajectory.
	//
	// The sink sees the RAW observation — it runs inside runTool, before the
	// AfterToolCall hook chain (LogSummaryHook / truncateMiddle) that bounds
	// the model-facing copy. A log.* tool can return tens of MB, so cap the
	// on-disk copy here to keep the JSONL from ballooning. Mask first (so a
	// secret near the cut is redacted regardless), then truncate.
	_ = s.sess.Append(session.Event{
		Kind: session.KindToolResult,
		Name: name,
		Text: truncateForLog(s.masker.MaskString(observation)),
	})
}

// maxPersistedObservationBytes bounds a single KindToolResult written to the
// session log. Generous enough to keep normal tool output intact, small
// enough that a runaway log dump can't balloon the JSONL.
const maxPersistedObservationBytes = 64 * 1024

// truncateForLog clips s to maxPersistedObservationBytes on a rune boundary,
// appending a marker so a reader knows the on-disk copy is partial.
func truncateForLog(s string) string {
	if len(s) <= maxPersistedObservationBytes {
		return s
	}
	cut := maxPersistedObservationBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…[truncated on disk]"
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
