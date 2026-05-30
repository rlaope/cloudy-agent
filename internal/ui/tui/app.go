package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rlaope/cloudy/internal/buildinfo"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/session"
)

// ctrlCTimeout is the window within which two Ctrl+C presses quit the program.
const ctrlCTimeout = 2 * time.Second

// compactAdviseThreshold is the context-window usage percent at and above
// which the TUI surfaces an amber "/compact recommended" hint below the
// prompt. Compaction stays manual; this only nudges the operator before
// accumulation degrades the turn.
const compactAdviseThreshold = 75

// compactAutoThreshold is the context-window usage percent at and above which
// /autocompact (when enabled) fires compaction automatically after a turn.
// Set above compactAdviseThreshold so the operator sees the amber nudge first.
const compactAutoThreshold = 90

// autoCompactMaxFails is the consecutive auto-compaction failure cap. Past it,
// auto-firing pauses until a success or a fresh /autocompact toggle, so a
// persistently-failing summarizer cannot spend a round-trip every turn.
const autoCompactMaxFails = 2

// compactAdviseStyle renders the below-prompt /compact hint in amber.
var compactAdviseStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)

// Splash, playback, thinking, self-update, and assorted style declarations
// live in splash.go, playback.go, thinking.go, selfupdate.go, and styles.go
// — sibling files in the same `tui` package. This file (app.go) owns the
// Model, the Update dispatch, and the View composition; the rest is split
// out so app.go does not become a single-file god-object.

// Deps holds all external dependencies the TUI needs.
type Deps struct {
	// Provider is the LLM backend. May be nil when no config is present.
	Provider llm.Provider
	// Model is the fully-qualified model identifier.
	Model string
	// Skills is the loaded skill registry.
	Skills *skills.Registry
	// Tools is the loaded tool registry.
	Tools *tools.Registry
	// Session is an open append-only session log (may be nil).
	Session *session.Session
	// InitialCtx is the current kubeconfig context (best-effort).
	InitialCtx string
	// InitialNS is the initial namespace (best-effort).
	InitialNS string
	// Contexts is the full list of kubeconfig contexts configured for this
	// session (cfg.Contexts when set; otherwise a single-element slice
	// containing InitialCtx, or empty when nothing is wired). The footer
	// renders this as `<default> +<N-1>` so the operator can see at a
	// glance which cluster(s) the agent talks to.
	Contexts []string
	// FirstRun is true when no config file exists yet, causing the TUI to
	// display the full welcome banner and prompt the user to run /setup.
	FirstRun bool
	// MaxTokensPerSession is the session-level token cap passed through to
	// the agent's CostGuardHook. Zero disables the check.
	MaxTokensPerSession int
	// MaxUSDPerDay is the rolling-day USD cap passed through to the agent's
	// CostGuardHook. Zero disables the check.
	MaxUSDPerDay float64
	// MaxConversationSeconds caps the wall-clock duration of a single agent
	// Run. Surfaced as ErrConversationTimeout when hit.
	MaxConversationSeconds int
	// MaxLogLinesPerCall caps the "limit" argument on log.* tool calls.
	MaxLogLinesPerCall int
	// MaxProfileSecondsPerCall caps duration_seconds on profiling tool calls.
	MaxProfileSecondsPerCall int
	// MaxLogResponseBytes is the byte ceiling at which a log.* observation
	// is rewritten as a head/tail + exception-context summary.
	MaxLogResponseBytes int
	// AgentRunner is the function called to run the agent on user input.
	// Injected by run.go so that tests can stub it. cancel is closed by the
	// TUI when the user cancels the in-flight request.
	AgentRunner func(cancel <-chan struct{}, input string, emit func(AgentEvent))
	// SwapModel hot-swaps the active LLM provider+model at runtime. /login
	// calls it after persisting the API key so the just-saved provider
	// becomes active for the next turn; /model <id> calls it directly.
	// Injected by run.go; tests may stub it with a recorder.
	SwapModel func(modelID string) error
	// CompactHistory folds the older portion of the conversation into one
	// summary message (a single LLM call), returning the summary text. /compact
	// calls it. Injected by run.go; may be nil in tests.
	CompactHistory func(ctx context.Context) (summary string, err error)
	// ResetHistory clears the conversation and rolls a fresh session file,
	// returning the new session id. /new calls it. Injected by run.go.
	ResetHistory func() (newSessionID string, err error)
	// SeedHistory loads a past conversation into the live history and rolls
	// the session to id so follow-up turns continue that same conversation.
	// /resume calls it after session.LoadHistory. Injected by run.go.
	SeedHistory func(id string, history []llm.Message) error
	// TogglePlan flips plan-first investigation (agent.Options.Plan) and
	// returns the new state. /plan calls it. Injected by run.go; may be nil
	// in tests (the handler then reports the feature as unavailable).
	TogglePlan func() (on bool)
}

// AgentEvent, ApprovalRequest, the tool/event message envelopes, and the
// usageAccum tracker live in agent_runner.go — the sibling file that owns
// the bubbletea-side controller for an in-flight agent turn (runAgent,
// pumpAgentCmd, applyAgentEvent). Same `tui` package, so the Update
// dispatch below references them as if they were declared here.

// Model is the root bubbletea model for the cloudy TUI.
type Model struct {
	header  HeaderModel
	stream  StreamModel
	prompt  PromptModel
	palette PaletteModel
	footer  FooterModel

	deps        Deps
	keys        keyMap
	activeSkill string

	// scope holds the current session scope set via /scope.
	scope Scope
	// usage accumulates token usage across agent runs.
	usage usageAccum

	// pendingApproval is set when the agent has emitted an ApprovalRequest
	// and the TUI is waiting on a y/n keystroke. Other keys are ignored
	// while this is set so an operator cannot drift past the decision.
	pendingApproval *ApprovalRequest

	// Ctrl+C double-tap state.
	lastCtrlC  time.Time
	ctrlCCount int

	// cancel is called to stop the in-flight agent goroutine.
	cancel func()
	// running indicates an agent run is in progress.
	running bool

	// autoCompact, when true, fires a /compact automatically once a finished
	// turn leaves context usage at or above compactAutoThreshold. Off by
	// default — the operator opts in with /autocompact; until then the amber
	// hint past compactAdviseThreshold only recommends it.
	autoCompact bool
	// autoCompactInFlight marks that the outstanding compaction was triggered
	// automatically (not by /compact), so compactDoneMsg can label it and
	// track failures.
	autoCompactInFlight bool
	// autoCompactPct snapshots the context percent at the moment auto-compaction
	// fired (LastInputTokens is zeroed by the time compactDoneMsg lands).
	autoCompactPct int
	// autoCompactFails counts consecutive failed auto-compactions; at the cap
	// auto-firing pauses so a persistently-failing summarizer can't burn a
	// round-trip every turn. Reset on a success or a fresh /autocompact toggle.
	autoCompactFails int

	// welcome renders the banner above an empty stream.
	welcome WelcomeModel

	width  int
	height int
	ready  bool

	// Boot splash: time-bounded brand screen shown before the main TUI.
	splash splashState

	// In-flight agent status. Drives the "✦ Thinking… (3s · 240 tokens)"
	// row rendered above the prompt while running == true.
	thinking thinkingState

	// Active inline conversation, if any. Mutually exclusive: only one
	// of these is non-nil at a time. submitMsg routes to whichever is
	// active before falling through to the agent.
	loginChat *loginChat
	setupChat *setupChat

	// Active arrow-key picker, set by chats that want a Claude-style
	// HITL menu instead of free-text input. While non-nil the parent
	// routes ↑/↓/Enter/Esc to the picker and swallows other keys.
	arrowPicker *arrowPicker

	// modelPickerActive is true while the operator is choosing a new
	// model via `/model` (with no argument). The picker's resolve
	// message is routed to Deps.SwapModel instead of a chat. Reset by
	// the resolve handler whether the operator confirmed or cancelled.
	modelPickerActive bool

	// scopePickerActive is true while the operator is choosing
	// namespaces via `/scope` (with no argument). The multi-select
	// picker's resolve message becomes a Scope{Namespaces: …} instead
	// of falling through to setupChat. Reset by the resolve handler.
	scopePickerActive bool

	// skillPickerActive is true while the operator is choosing a skill
	// via bare `/skill` (no argument). The picker's resolve message
	// activates the selected skill via the same path as `/skill <name>`.
	// Reset by the resolve handler whether the operator confirmed or cancelled.
	skillPickerActive bool

	// agentCh is the channel the in-flight agent goroutine emits
	// AgentEvent values on. The Update loop reads it one event at a
	// time via pumpAgentCmd — after each event is applied to sub-
	// models, applyAgentEvent re-issues the pump so the next event
	// is delivered. Without this re-pump tokens 2..N would sit in
	// the buffered channel forever and the user would see the
	// thinking timer tick but no actual response text.
	agentCh chan agentEventMsg

	// assistantTurnStarted flips to true on the first Token event of
	// the current turn so applyAgentEvent can prepend the "● "
	// assistant glyph exactly once per response. Reset on each new
	// submitMsg and on agentDoneMsg so the next turn re-emits the
	// affordance — without that anchor the agent text materialises
	// right against the "> <user>" echo with no visual separation.
	assistantTurnStarted bool

	// playbackBuf accumulates assistant runes (after indent
	// transformation) that have not yet been emitted to the stream.
	// The playbackTick ticker pops a bounded number of runes per
	// frame and writes them via streamWriteMsg, producing a steady
	// "typewriter" cadence regardless of how bursty the upstream
	// LLM stream is. Stored as []rune so multi-byte characters
	// (Korean, emoji, etc.) never get cut mid-rune at the slice
	// boundary, which would emit invalid UTF-8 to the terminal.
	playbackBuf []rune

	// playbackActive is true while a playbackTickMsg loop is in
	// flight. Guards against scheduling duplicate ticks when several
	// token events arrive in the same frame.
	playbackActive bool

	// playbackEmittable is the number of runes at the front of
	// playbackBuf that have been cleared for the typewriter to drain.
	// Advanced by bufferAssistantToken whenever a new sentence /
	// clause / paragraph boundary lands in the buffer (see
	// refreshEmittableWindow). The look-ahead gate makes sure the
	// typewriter only starts emitting chars whose end-of-unit is
	// already buffered, so a sentence never appears half-typed while
	// the next SSE chunk is in flight.
	playbackEmittable int

	// fullscreen mirrors run.go's CLOUDY_FULLSCREEN opt-in (alt-screen +
	// mouse capture). In alt-screen mode tea.Println is a no-op, so the
	// native-scrollback path (commit finished turns into the terminal's
	// real scrollback) would silently drop them; fullscreen therefore
	// keeps the whole transcript in the scrollable in-app viewport
	// instead — the wheel scrolls within it because the mouse is
	// captured. Set once by Run.
	fullscreen bool

	// introPrinted guards the one-time header + welcome banner print so a
	// key-skip and the splash timer don't both emit it into scrollback.
	introPrinted bool

	// pendingUserEcho is the pre-rendered "queued chip" column shown
	// directly above the prompt between submit and the first agent
	// event of the turn. Multiple submits while the agent is busy
	// stack into the same string (Claude Code lets the operator queue
	// follow-up questions while a reply is in flight). Drained as one
	// block into the stream by flushPendingUserEcho.
	pendingUserEcho string
}

// NewModel constructs the root TUI model.
func NewModel(deps Deps) Model {
	keys := defaultKeys()
	noColor := false

	state := footerClusterState(deps.Contexts, deps.InitialCtx)
	if deps.FirstRun {
		state = footerStateUnconfigured
	}

	return Model{
		header:  newHeaderModel(deps.InitialCtx, deps.InitialNS, deps.Model),
		stream:  newStreamModel(noColor),
		prompt:  newPromptModel(keys),
		palette: newPaletteModel(),
		footer:  NewFooterModel(state, deps.Model, buildinfo.Version),
		deps:    deps,
		keys:    keys,
		cancel:  func() {},
		welcome: NewWelcomeModel(deps.FirstRun, deps.InitialCtx),
		splash:  splashState{start: time.Now()},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.prompt.Init(), splashTickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.MouseMsg:
		// Mouse wheel scrolls the stream viewport's transcript. We
		// deliberately do NOT forward MouseMsg to the prompt (the
		// bubbles/textarea inside it interprets wheel-as-arrow-keys
		// and would otherwise hijack scroll-up to mean "navigate
		// history", which is the bug this branch exists to fix).
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(msg)
		return m, sCmd

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

		var hCmd, sCmd, prCmd, paCmd tea.Cmd
		m.header, hCmd = m.header.Update(msg)
		m.stream, sCmd = m.stream.Update(msg)
		m.prompt, prCmd = m.prompt.Update(msg)
		m.palette, paCmd = m.palette.Update(msg)
		m.footer.SetWidth(msg.Width)
		m.welcome.SetWidth(msg.Width)
		return m, tea.Batch(hCmd, sCmd, prCmd, paCmd)

	case splashTickMsg:
		if m.splash.done {
			// Already dismissed (e.g. by a key-skip); stop the tick loop so
			// it doesn't re-print the intro once the timer elapses.
			return m, nil
		}
		m.splash.frame++
		if time.Since(m.splash.start) >= splashDuration {
			m.splash.done = true
			// Print the one-time header + welcome banner into native
			// scrollback. They scroll away with the transcript instead of
			// pinning chrome above the live region — the conversation flows
			// into the terminal's real scrollback from here on.
			return m, m.maybePrintIntro()
		}
		return m, splashTickCmd()

	case thinkingTickMsg:
		if !m.running {
			// Agent finished and the body has been drained in the
			// same Update that fired agentDoneMsg — no playback tail
			// to wait on anymore, so the elapsed counter retires
			// here instead of running on borrowed time.
			return m, nil
		}
		m.thinking.tick()
		return m, thinkingTickCmd()

	case playbackTickMsg:
		// Post-done typewriter drain. Each tick pops a chunk of runes
		// off playbackBuf and writes them to the stream; the loop
		// keeps re-arming itself until the buffer is empty, at which
		// point playbackActive drops to false and no further ticks
		// are scheduled. Ticks that arrive after a cancel/clear (or
		// after a ToolBegin force-drain) find playbackActive=false
		// and are a no-op.
		if !m.playbackActive {
			return m, nil
		}
		chunk := m.popPlaybackRunes(playbackRunesPerTick)
		if chunk == "" {
			if len(m.playbackBuf) == 0 {
				// Turn fully played back — commit it to native scrollback
				// and collapse the live viewport so the next turn starts
				// fresh below the committed history.
				m.playbackActive = false
				return m, m.commitTurn()
			}
			// Emittable window momentarily closed; keep the loop alive so
			// the remaining tail still drains.
			return m, playbackTickCmd()
		}
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamWriteMsg(chunk))
		if len(m.playbackBuf) == 0 {
			m.playbackActive = false
			return m, tea.Batch(sCmd, m.commitTurn())
		}
		return m, tea.Batch(sCmd, playbackTickCmd())

	case tea.KeyMsg:
		// Splash key-skip: an eager typist who hits Enter / starts a
		// /command during the boot brand should not have to wait for
		// the timer. Mark it done and seed the welcome banner the
		// same way splashTickMsg would, then fall through so the
		// keystroke itself reaches the prompt on the same frame. The
		// returned sCmd from a streamWriteMsg is the viewport's
		// passthrough cmd (nil for non-key/mouse messages), so it is
		// safely discarded here.
		if !m.splash.done {
			m.splash.done = true
			// Print the intro into scrollback, then re-enter Update with the
			// same key now that the splash is gone so it gets normal
			// same-frame handling. Recursion is one level deep (the branch
			// is skipped on re-entry) and avoids threading the intro cmd
			// through every early return below.
			introCmd := m.maybePrintIntro()
			nextModel, keyCmd := m.Update(msg)
			// Sequence (not Batch): the intro must print BEFORE any
			// scrollback the re-entered key produces, and two tea.Println
			// cmds in a Batch have no ordering guarantee.
			return nextModel, tea.Sequence(introCmd, keyCmd)
		}

		// Arrow picker takes the screen first: while a Claude-style HITL
		// menu is open, ↑/↓/Enter/Esc are routed to the picker and all
		// other keys are swallowed so the operator cannot accidentally
		// drift past the decision. Ctrl+C still escalates so the global
		// cancel/quit shortcut keeps working.
		if m.arrowPicker != nil {
			switch msg.String() {
			case "up", "k":
				m.arrowPicker.MoveUp()
				return m, nil
			case "down", "j":
				m.arrowPicker.MoveDown()
				return m, nil
			case " ":
				// Space is the multi-select toggle. No-op on single-select
				// pickers (Toggle short-circuits), so this is always safe
				// to route through unconditionally.
				m.arrowPicker.Toggle()
				return m, nil
			case "enter":
				if m.arrowPicker.multiSelect {
					keys := m.arrowPicker.SelectedKeys()
					return m, arrowPickerMultiResolveCmd(keys, false)
				}
				sel := m.arrowPicker.Selected()
				return m, arrowPickerResolveCmd(sel.key, false)
			case "esc":
				if m.arrowPicker.multiSelect {
					return m, arrowPickerMultiResolveCmd(nil, true)
				}
				return m, arrowPickerResolveCmd("", true)
			case "ctrl+c":
				// Cancel the picker first, then fall through to the
				// standard ctrl+c handler below.
				m.arrowPicker = nil
			default:
				return m, nil
			}
		}

		// Palette is active. Nav keys go to the palette; text keys flow into
		// the prompt so the user can keep typing past the '/', and the
		// palette then refilters against the new prompt value.
		if m.palette.Active() {
			switch msg.String() {
			case "up", "down", "enter", "tab", "esc", "ctrl+c":
				var cmd tea.Cmd
				m.palette, cmd = m.palette.Update(msg)
				return m, cmd
			}
			var prCmd tea.Cmd
			m.prompt, prCmd = m.prompt.Update(msg)
			val := m.prompt.Value()
			if strings.HasPrefix(val, "/") {
				m.palette.Refilter(val)
			} else {
				m.palette.Close()
			}
			return m, prCmd
		}

		// Approval gate has priority over normal key handling: while a
		// RiskHigh tool is awaiting a decision, only y/n/Esc/Ctrl+C are
		// honoured. Other keys are swallowed so an operator cannot
		// accidentally page past the prompt.
		if m.pendingApproval != nil {
			switch msg.String() {
			case "y", "Y":
				m.pendingApproval.Reply <- true
				m.pendingApproval = nil
				return m, nil
			case "n", "N", "esc":
				m.pendingApproval.Reply <- false
				m.pendingApproval = nil
				return m, nil
			case "ctrl+c":
				// Fall through to the existing Ctrl+C handler — cancel
				// propagates through ctx.Done(), the agent goroutine
				// releases on its own, and we drop the pending request.
				m.pendingApproval = nil
			default:
				return m, nil
			}
		}

		switch msg.String() {
		case "ctrl+c":
			now := time.Now()
			if now.Sub(m.lastCtrlC) <= ctrlCTimeout {
				m.ctrlCCount++
			} else {
				m.ctrlCCount = 1
			}
			m.lastCtrlC = now

			if m.ctrlCCount >= 2 {
				return m, tea.Quit
			}
			// Single Ctrl+C: cancel in-flight request. Discard any
			// buffered playback runes — the operator wants out, not a
			// drawn-out typewriter drain of work they just abandoned.
			m.cancel()
			m.cancel = func() {}
			m.running = false
			m.thinking.streaming = false
			m.prompt.SetInFlight(false)
			m.assistantTurnStarted = false
			m.playbackBuf = m.playbackBuf[:0]
			m.playbackEmittable = 0
			m.playbackActive = false
			// Move the queued user chip + any in-flight turn content into
			// native scrollback before returning to idle — otherwise the
			// operator's question (and partial output) vanishes on Ctrl+C.
			var cancelCmds []tea.Cmd
			if flush := m.flushPendingUserEcho(); flush != "" {
				var sCmd tea.Cmd
				m.stream, sCmd = m.stream.Update(streamWriteMsg(flush))
				cancelCmds = append(cancelCmds, sCmd)
			}
			if c := m.commitTurn(); c != nil {
				cancelCmds = append(cancelCmds, c)
			}
			return m, tea.Batch(cancelCmds...)

		case "ctrl+l":
			// Reset the assistant-turn anchor so the next agent token
			// after a clear gets a fresh "● " bullet, and drop any
			// playback runes still in flight — the operator just
			// wiped the screen on purpose; keep-typing-the-old-reply
			// would defeat that intent. Same reasoning for the
			// queued user chip: a deliberate clear should not leak a
			// "queued question" onto the freshly-blank screen.
			m.assistantTurnStarted = false
			m.playbackBuf = m.playbackBuf[:0]
			m.playbackEmittable = 0
			m.playbackActive = false
			m.pendingUserEcho = ""
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamClearMsg{})
			// Native scrollback can't be un-printed, but clearing the
			// visible screen gives the operator the "fresh slate" the
			// command promises; scrolled-back history stays reachable.
			return m, tea.Batch(sCmd, tea.ClearScreen)

		case "pgup":
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(msg)
			return m, sCmd

		case "pgdown":
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(msg)
			return m, sCmd

		case "esc":
			return m, m.handleEscape()

		case "tab":
			// Open palette with current prompt content.
			val := m.prompt.Value()
			if !strings.HasPrefix(val, "/") {
				m.prompt.SetValue("/")
				val = "/"
			}
			m.palette.Open(val)
			return m, nil

		case "enter":
			val := strings.TrimSpace(m.prompt.Value())
			if val == "" {
				return m, nil
			}
			if strings.HasPrefix(val, "/") {
				m.palette.Open(val)
				return m, nil
			}
			// Fall through to prompt update which emits submitMsg.

		case "up", "down":
			// Let prompt handle history navigation only when palette is closed.
			var pCmd tea.Cmd
			m.prompt, pCmd = m.prompt.Update(msg)
			return m, pCmd
		}

	case submitMsg:
		val := string(msg)

		// Inline conversations (e.g. /login, /setup) capture every plain
		// submit until they signal done. Slash commands still resolve
		// through the palette path below, so the operator can /cancel a
		// flow with /quit or similar without typing into the prompt.
		if m.loginChat != nil && !strings.HasPrefix(val, "/") {
			res := m.loginChat.Step(val)
			return m, m.applyLoginResult(res)
		}
		if m.setupChat != nil && !strings.HasPrefix(val, "/") {
			res := m.setupChat.Step(val)
			if res.done {
				m.setupChat = nil
			}
			cmds := []tea.Cmd{m.writeStream(res.out)}
			if res.cmd != nil {
				cmds = append(cmds, res.cmd)
			}
			return m, tea.Batch(cmds...)
		}

		if strings.HasPrefix(val, "/scope ") {
			return m, m.handleScopeCmd(strings.TrimPrefix(val, "/scope "))
		}
		if val == "/setup" || strings.HasPrefix(val, "/setup ") {
			return m, m.enterSetup()
		}

		// If the previous turn's answer is still typing out (playback
		// active) when a new question arrives, finalise it first: commitTurn
		// drains the remaining buffer into the live viewport, commits the
		// turn to native scrollback, and collapses the viewport. Without
		// this the old reply dumps into and interleaves with the new turn —
		// the "answer suddenly pops out, then shows up again below the new
		// question" unnaturalness. commitTurn is a no-op when idle.
		var finalizeCmds []tea.Cmd
		if c := m.commitTurn(); c != nil {
			finalizeCmds = append(finalizeCmds, c)
		}

		// Queue the echo as a chip above the prompt; flush into the
		// stream on the first agent event of the turn. += (not =)
		// so a second submit while the agent is still working stacks
		// below the first, matching Claude Code's "you can keep
		// typing follow-ups while a reply is in flight" behaviour.
		echo := userEchoStyle.Render("> " + val)
		m.pendingUserEcho += "\n" + echo + "\n"

		// Setup gate: refuse to dispatch when no model has been picked.
		// We deliberately don't gate on m.deps.Provider — that field is a
		// stale snapshot from startup. The live provider lives inside the
		// agent runner's providerRef, swapped via /login or /model; m.deps.Model
		// is the only field updated by those swaps, so it's the truth here.
		if m.deps.Model == "" {
			// No model picked: the agent will never fire, so there is
			// no "first event" to drain the chip on. Flush it now,
			// alongside the red banner, so the operator still sees
			// what they asked plus why nothing happened.
			flush := m.flushPendingUserEcho()
			warn := setupRequiredStyle.Render(
				"⚠ no LLM model selected. Run /login to pick a provider, paste "+
					"an API key, and choose a model — chat works without /setup; "+
					"/setup only adds read-only infrastructure probes (k8s, prom, "+
					"loki, …) so cloudy can investigate questions about your clusters.",
			) + "\n"
			var wCmd tea.Cmd
			m.stream, wCmd = m.stream.Update(streamWriteMsg(flush + warn))
			finalizeCmds = append(finalizeCmds, wCmd)
			if c := m.commitTurn(); c != nil {
				finalizeCmds = append(finalizeCmds, c)
			}
			return m, tea.Batch(finalizeCmds...)
		}

		// Start the in-flight thinking animation. Reset seeds a new
		// verb and zeroes the streaming counters.
		m.thinking.reset()
		// New turn — re-arm the "first token gets ● " hook so the
		// upcoming response is properly anchored.
		m.assistantTurnStarted = false
		// Visual feedback that the system is working: the prompt
		// border switches to the brand sky-blue while a request is
		// in flight. Cleared in agentDoneMsg and on Esc/Ctrl+C cancel.
		m.prompt.SetInFlight(true)

		finalizeCmds = append(finalizeCmds, m.runAgent(val), thinkingTickCmd())
		return m, tea.Batch(finalizeCmds...)

	case arrowPickerResolveMsg:
		// Single-select picker — could be /login (provider OR model step),
		// the standalone /model picker, or the standalone /skill picker.
		// Route by which mode is active.
		m.arrowPicker = nil
		input := msg.key
		if msg.cancelled {
			input = "cancel"
		}
		if m.loginChat != nil {
			res := m.loginChat.Step(input)
			return m, m.applyLoginResult(res)
		}
		if m.modelPickerActive {
			m.modelPickerActive = false
			if msg.cancelled {
				return m, m.writeStream("[model swap cancelled]\n")
			}
			return m, m.applyModelSwap(input)
		}
		if m.skillPickerActive {
			m.skillPickerActive = false
			if msg.cancelled {
				return m, m.writeStream("[skill selection cancelled]\n")
			}
			// Route through the same activation path as `/skill <name>`.
			return m, m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: msg.key})
		}
		return m, nil

	case arrowPickerMultiResolveMsg:
		// Multi-select picker. Currently driven by /setup (contexts +
		// backend kinds) and /scope (namespaces). Route by which mode
		// is active so the two flows do not collide.
		m.arrowPicker = nil
		if m.scopePickerActive {
			m.scopePickerActive = false
			if msg.cancelled {
				return m, m.writeStream("[scope cancelled]\n")
			}
			// "↺ All namespaces" wins regardless of what else the
			// operator ticked — it's the explicit affordance for
			// "drop the current scope". Same outcome as an empty
			// selection, but discoverable instead of "deselect
			// everything and pray".
			ns := make([]string, 0, len(msg.keys))
			for _, k := range msg.keys {
				if k == scopeResetKey {
					return m, m.handleScopeCmd("reset")
				}
				ns = append(ns, k)
			}
			if len(ns) == 0 {
				return m, m.handleScopeCmd("reset")
			}
			return m, m.handleScopeCmd("ns=" + strings.Join(ns, ","))
		}
		if m.setupChat != nil {
			res := m.setupChat.ApplyMulti(msg.keys, msg.cancelled)
			return m, m.applySetupResult(res)
		}
		return m, nil

	case scopeNamespacesMsg:
		if !m.scopePickerActive {
			// Stale result — operator cancelled or moved on before
			// kubectl returned. Drop it silently so we don't pop
			// a picker over whatever the operator is doing now.
			return m, nil
		}
		if msg.err != nil {
			m.scopePickerActive = false
			return m, m.writeStream(agentError("scope", msg.err))
		}
		if len(msg.namespaces) == 0 {
			m.scopePickerActive = false
			return m, m.writeStream("[scope: no namespaces visible from current credentials]\n")
		}
		m.arrowPicker = buildScopeNamespacePicker(msg.namespaces, m.scope.Namespaces)
		return m, nil

	case paletteActionMsg:
		return m, m.handlePaletteAction(msg)

	case paletteDismissMsg:
		// Palette closed without action. Drop the leading '/' only when the
		// operator is still on the verb (no arguments typed yet) so a typo
		// like "/scope ns=foo" + Esc doesn't wipe the whole line.
		val := m.prompt.Value()
		if strings.HasPrefix(val, "/") && !strings.ContainsRune(val, ' ') {
			m.prompt.SetValue("")
		}
		return m, nil

	case paletteEscalateMsg:
		// Palette forwarded a key it does not handle (currently only Ctrl+C).
		// Re-apply the key to the main Update so Ctrl+C cancels the request
		// or double-tap-quits the program regardless of palette state.
		return m.Update(msg.key)

	case agentEventMsg:
		// Apply this event to the sub-models, AND re-arm the channel
		// pump so the next event is delivered. Without the pump cmd
		// here the channel would block after the first token and the
		// operator would see the thinking timer tick forever with no
		// actual response text.
		evt := AgentEvent(msg)
		return m, tea.Batch(m.applyAgentEvent(evt), m.pumpAgentCmd())

	case setupScanDoneMsg:
		if m.setupChat == nil {
			return m, nil
		}
		// Apply returns a setupResult that may carry the findings picker
		// (when backends were discovered) or kick straight to save (when
		// none were). applySetupResult handles both shapes uniformly.
		return m, m.applySetupResult(m.setupChat.Apply(msg))

	case setupSaveDoneMsg:
		if m.setupChat == nil {
			return m, nil
		}
		res := m.setupChat.Apply(msg)
		if res.done {
			m.setupChat = nil
		}
		// Save succeeded: refresh the footer state segment AND deps.Contexts
		// from the just-written cloudy.yaml so the footer reads the chosen
		// cluster(s) — not the stale boot-time snapshot — and reset the
		// welcome banner for the returning-user form.
		if msg.err == nil {
			if cfg, err := config.Load(config.Path()); err == nil {
				m.deps.Contexts = cfg.Contexts
			}
			m.footer.SetState(footerClusterState(m.deps.Contexts, m.deps.InitialCtx))
			m.welcome = NewWelcomeModel(false, m.deps.InitialCtx)
			m.welcome.SetWidth(m.width)
		}
		return m, m.writeStream(res.out)

	case agentDoneMsg:
		m.running = false
		m.prompt.SetInFlight(false)
		// Release the channel so a stray late pump-cmd from the previous
		// event doesn't keep this goroutine alive or block on a closed
		// channel. Subsequent runAgent calls install a fresh ch.
		m.agentCh = nil
		// If the run produced zero events (immediate error, empty
		// response) the chip is still queued — flush it now so the
		// question is preserved in the transcript.
		echoFlush := m.flushPendingUserEcho()
		if msg.err != nil {
			// Errors short-circuit playback: dump any buffered tokens
			// first so they aren't lost, then surface the error so
			// the operator sees the full prose + the diagnostic
			// rather than half a sentence cut off by a red banner.
			drain := m.drainPlaybackBuffer()
			m.playbackActive = false
			m.thinking.streaming = false
			// Assemble echo + partial prose + the error as ONE block in the
			// live viewport, then commit it to native scrollback so the
			// whole failed turn lands together (and in order) instead of the
			// error racing ahead of the prose via a separate print.
			out := echoFlush
			if drain != "" {
				out += m.assistantBulletPrefix() + drain
			}
			out += "\n" + agentError("error", msg.err)
			m.assistantTurnStarted = false
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamWriteMsg(out))
			return m, tea.Batch(sCmd, m.commitTurn())
		}
		// Happy path: render the buffered markdown through glamour so
		// headings / bold / code fences / lists land as terminal-
		// styled text rather than literal #/`/* syntax, then start a
		// fast typewriter drain of the rendered output. The body was
		// queued silently during streaming so the user only saw the
		// spinner; once Done arrives the bullet AND the body flow in
		// together at ~1500 chars/sec — the bullet no longer sits
		// alone on screen for the full streaming duration.
		m.thinking.streaming = false
		if len(m.playbackBuf) > 0 {
			raw := string(m.playbackBuf)
			rendered := m.stream.RenderAssistantMarkdown(raw)
			rendered = strings.TrimRight(rendered, "\n")
			rendered = indentRenderedBlock(rendered)
			// Prepend the bullet so it lands at the same moment the
			// body starts flowing rather than the first-Token moment
			// when only the spinner had something visible to show.
			// Append two newlines so the next user prompt (or tool
			// block) gets a blank-line separator below the reply,
			// matching the Claude Code transcript rhythm.
			rendered = m.assistantBulletPrefix() + rendered + "\n\n"
			m.playbackBuf = []rune(rendered)
		}
		m.assistantTurnStarted = false
		m.releasePlaybackTail()
		var cmds []tea.Cmd
		// The turn is done and not running, so context usage is settled — fire
		// auto-compaction now if it is enabled and the window crossed the
		// threshold. compactCmd is async; the CAS guard in CompactHistory
		// protects it from any racing mutation.
		if ac := m.maybeAutoCompactCmd(); ac != nil {
			cmds = append(cmds, ac)
		}
		if echoFlush != "" {
			var fCmd tea.Cmd
			m.stream, fCmd = m.stream.Update(streamWriteMsg(echoFlush))
			cmds = append(cmds, fCmd)
		}
		if len(m.playbackBuf) == 0 {
			// No prose to type out (e.g. a tool-only turn): commit whatever
			// landed in the live viewport (echo + tool blocks) straight to
			// native scrollback.
			m.playbackActive = false
			cmds = append(cmds, m.commitTurn())
			return m, tea.Batch(cmds...)
		}
		m.playbackActive = true
		cmds = append(cmds, playbackTickCmd())
		return m, tea.Batch(cmds...)

	case selfUpdateDoneMsg:
		// Dump the captured progress log first so the operator sees
		// the same trace they would have seen at the CLI, then a
		// terse summary line. On success we also remind them to
		// restart — Unix lets us atomically swap the binary while
		// it is running, but the current process keeps the OLD
		// inode mapped until it exits.
		var b strings.Builder
		if msg.log != "" {
			b.WriteString(msg.log)
		}
		if msg.err != nil {
			b.WriteString("\n")
			b.WriteString(agentError("update", msg.err))
		} else if msg.result.Replaced {
			fmt.Fprintf(&b, "\n✓ updated %s → %s — restart cloudy (Ctrl+C twice or /exit) to use it.\n",
				msg.result.PreviousVersion, msg.result.LatestVersion)
		}
		return m, m.writeStream(b.String())

	case compactDoneMsg:
		auto := m.autoCompactInFlight
		m.autoCompactInFlight = false
		if msg.err != nil {
			if auto {
				// Count the failure so a persistently-failing summarizer stops
				// retrying every turn; surface that auto-compact paused.
				m.autoCompactFails++
				note := ""
				if m.autoCompactFails >= autoCompactMaxFails {
					note = " — auto-compact paused; run /compact or toggle /autocompact to retry"
				}
				return m, m.writeStream(agentError("auto-compact", msg.err) + note + "\n")
			}
			return m, m.writeStream(agentError("compact", msg.err))
		}
		// History shrank: zero the gauge optimistically so the operator sees
		// immediate effect; the next turn's usage event sets the true value.
		m.usage.LastInputTokens = 0
		lead := "✓ compacted"
		if auto {
			m.autoCompactFails = 0
			lead = fmt.Sprintf("✓ auto-compacted at %d%% context", m.autoCompactPct)
		}
		return m, m.writeStream(fmt.Sprintf(
			"%s — kept the last %d messages, folded the rest into a summary:\n\n%s\n",
			lead, compactKeepMessages, msg.summary))

	case agentUsageMsg:
		m.usage.Input += msg.Input
		m.usage.Output += msg.Output
		m.usage.USD += msg.USD
		if msg.Input > 0 {
			m.usage.LastInputTokens = msg.Input
		}
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{cost: msg.USD})
		return m, hCmd

	case headerStateMsg:
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(msg)
		return m, hCmd
	}

	// Default: route to prompt and stream.
	var prCmd, sCmd tea.Cmd
	m.prompt, prCmd = m.prompt.Update(msg)
	m.stream, sCmd = m.stream.Update(msg)
	cmds = append(cmds, prCmd, sCmd)

	// Open palette if prompt value starts with '/'.
	if !m.palette.Active() && strings.HasPrefix(m.prompt.Value(), "/") {
		m.palette.Open(m.prompt.Value())
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}

	// Boot splash takes over the screen for splashDuration milliseconds so
	// the brand banner lands before the operator starts typing.
	if !m.splash.done {
		return m.renderSplash()
	}

	prompt := m.prompt.View()
	paletteView := m.palette.View()
	// The header is printed once into scrollback at startup, so the live
	// cost readout it used to own now rides the pinned footer.
	m.footer.SetCost(m.usage.USD)
	ctxPct := m.contextPct()
	m.footer.SetCtxPct(ctxPct)
	footer := m.footer.View()

	// Amber /compact hint, shown directly under the prompt once context
	// usage crosses the advise threshold. Compaction stays manual — this
	// is a nudge, not an auto-trigger. Empty below threshold so the slot
	// collapses to height 0 (same pattern as the optional approval banner).
	compactHint := ""
	if ctxPct >= compactAdviseThreshold {
		compactHint = compactAdviseStyle.Render(
			fmt.Sprintf("⚠ context %d%% full — type /compact to free it (or /new to start fresh)", ctxPct))
	}
	thinking := m.renderThinkingRow()
	pickerView := ""
	if m.arrowPicker != nil {
		pickerView = m.arrowPicker.View()
	}

	// Approval banner sits directly above the prompt (Claude-style) so the
	// operator cannot miss it. Composed at View time so the agent goroutine
	// never has to touch the stream's Builder.
	var banner string
	if m.pendingApproval != nil {
		line1 := fmt.Sprintf("⚠ approval required: %s(%s) — RiskHigh",
			m.pendingApproval.Tool, m.pendingApproval.Args)
		line2 := "  press [y] to approve, [n] or Esc to deny"
		banner = approvalBannerStyle.Render(line1) + "\n" + approvalHintStyle.Render(line2)
	}

	// Queued user chip: pre-rendered echo of the operator's just-
	// submitted prompt that lives in its own slot directly above the
	// prompt textarea until the agent emits its first event. Empty
	// string most of the time, so the slot collapses to height 0
	// (just like the optional approval banner).
	queuedEcho := strings.TrimRight(m.pendingUserEcho, "\n")

	// Compute the body height by subtracting every other component's actual
	// rendered height from the terminal height. lipgloss.Height counts rows
	// correctly even when content wraps, so this stays correct in narrow
	// split panes.
	//
	// chromeBottomPad reserves two extra rows below the footer: one as
	// breathing room between the prompt and the footer, one as bottom
	// padding so the TUI doesn't sit flush against the terminal edge.
	const chromeBottomPad = 2
	promptH := lipgloss.Height(prompt)
	paletteH := lipgloss.Height(paletteView)
	footerH := lipgloss.Height(footer)
	bannerH := 0
	if banner != "" {
		bannerH = lipgloss.Height(banner)
	}
	// renderThinkingRow always returns content (`· ready` when idle,
	// the live `✦ …` form while running), so the row is part of the
	// layout unconditionally. Dropping the `if thinking != ""` guard
	// here keeps the slot reserved even if a future edit accidentally
	// returns "" — preserving the "no prompt jump per turn" promise.
	thinkingH := lipgloss.Height(thinking)
	pickerH := 0
	if pickerView != "" {
		pickerH = lipgloss.Height(pickerView)
	}
	queuedH := 0
	if queuedEcho != "" {
		queuedH = lipgloss.Height(queuedEcho)
	}
	compactHintH := 0
	if compactHint != "" {
		compactHintH = lipgloss.Height(compactHint)
	}
	// streamBottomMargin reserves one row of breathing space between
	// the last line of the transcript and the chrome (banner / thinking
	// row / prompt). Without it the agent's final character lands flush
	// against the prompt border, which reads as "cloudy is stuck" even
	// when the turn finished cleanly.
	const streamBottomMargin = 1
	bodyBudget := m.height - promptH - paletteH - footerH - bannerH - thinkingH - pickerH - queuedH - compactHintH - chromeBottomPad - streamBottomMargin
	if bodyBudget < 1 {
		bodyBudget = 1
	}
	// The live region holds only the IN-FLIGHT turn; completed turns live
	// in native terminal scrollback (printed via tea.Println on commit).
	// Size the viewport to the current turn's content height, capped at the
	// budget, so it collapses to nothing between turns and the prompt rides
	// directly under the committed history instead of floating below a
	// tall, mostly-blank window. A turn taller than the budget caps here and
	// scrolls internally (GotoBottom keeps the latest line visible).
	body := ""
	if m.fullscreen {
		// Alt-screen: the viewport owns the whole transcript and the
		// mouse wheel scrolls within it, so give it the full body budget.
		m.stream.SetViewportSize(m.width, bodyBudget)
		body = m.stream.View()
	} else if ch := m.stream.ContentHeight(); ch > 0 {
		bodyH := ch
		if bodyH > bodyBudget {
			bodyH = bodyBudget
		}
		m.stream.SetViewportSize(m.width, bodyH)
		body = m.stream.View()
	}

	// Composed bottom-up: in-flight body → blank margin → optional approval
	// banner → thinking row → optional queued chip → prompt → optional
	// palette suggestions → blank separator → status footer → blank bottom
	// padding. The header + completed turns already scrolled into native
	// scrollback above this live frame.
	parts := []string{}
	if body != "" {
		parts = append(parts, body, "")
	}
	if banner != "" {
		parts = append(parts, banner)
	}
	// Same reasoning as the thinkingH calculation above — unconditional
	// inclusion guards the layout slot against accidental empty-string
	// regressions in renderThinkingRow.
	parts = append(parts, thinking)
	if pickerView != "" {
		parts = append(parts, pickerView)
	}
	if queuedEcho != "" {
		parts = append(parts, queuedEcho)
	}
	parts = append(parts, prompt)
	if compactHint != "" {
		parts = append(parts, compactHint)
	}
	if paletteView != "" {
		parts = append(parts, paletteView)
	}
	parts = append(parts, "", footer, "")

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// contextPct reports the current context-window usage as a 0-100 percent:
// the latest turn's input-token count over the active model's context
// window. Returns 0 before the first usage event (LastInputTokens == 0),
// so the gauge reads "ctx 0%" on a fresh session. Drives the footer gauge
// and the below-prompt /compact hint.
func (m *Model) contextPct() int {
	if m.usage.LastInputTokens <= 0 {
		return 0
	}
	win := llm.ContextWindow(m.deps.Model)
	if win <= 0 {
		return 0
	}
	pct := 100 * m.usage.LastInputTokens / win
	if pct > 100 {
		pct = 100
	}
	return pct
}

// maybeAutoCompactCmd returns a bare compactCmd when /autocompact is enabled
// and the just-finished turn left context usage at or above
// compactAutoThreshold, or nil otherwise. Called from the agentDoneMsg handler
// where the turn is settled and not running.
//
// It deliberately does NOT write to the stream: the Done handler has loaded but
// not yet activated the playback buffer, and a stream write here would
// force-drain that buffer and skip the reply's typewriter animation. The
// "auto-compacting" notice is emitted by the compactDoneMsg handler instead,
// which runs after playback is underway.
//
// After a successful compaction LastInputTokens is zeroed, so contextPct drops
// and this does not re-fire until the window climbs again. After
// autoCompactMaxFails consecutive failures it pauses until a success or a
// fresh /autocompact toggle.
func (m *Model) maybeAutoCompactCmd() tea.Cmd {
	if !m.autoCompact || m.deps.CompactHistory == nil {
		return nil
	}
	if m.autoCompactFails >= autoCompactMaxFails {
		return nil
	}
	pct := m.contextPct()
	if pct < compactAutoThreshold {
		return nil
	}
	m.autoCompactInFlight = true
	m.autoCompactPct = pct
	return compactCmd(m.deps.CompactHistory)
}

// writeStream is the every-other-palette-action shape: clear the prompt
// and append text to the stream output. Extracted because the same three
// lines appeared in 11 branches of handlePaletteAction; collapsing them
// makes the dispatcher scannable.
//
// Prints the chrome line directly into native terminal scrollback via
// tea.Println so it persists, is mouse-wheel scrollable, and is
// drag-selectable like the rest of the transcript. If an in-flight turn
// left uncommitted content in the live viewport, that is flushed FIRST
// (concatenated into the same print) so ordering is preserved — two
// separate tea.Println cmds in a Batch are not order-guaranteed.
func (m *Model) writeStream(s string) tea.Cmd {
	m.prompt.SetValue("")
	if m.fullscreen {
		// Alt-screen keeps the transcript in the in-app viewport.
		var c tea.Cmd
		m.stream, c = m.stream.Update(streamWriteMsg(s))
		return c
	}
	// A slash command / picker / async-setup result can land while a prior
	// reply is still typing out; finalise it first so its tail commits with
	// it instead of leaking out below this chrome on a later tick.
	m.finalizeActivePlayback()
	body := strings.TrimRight(s, "\n")
	pending := strings.TrimRight(m.stream.Commit(), "\n")
	var combined string
	switch {
	case pending != "" && body != "":
		combined = pending + "\n" + body
	case pending != "":
		combined = pending
	default:
		combined = body
	}
	if combined == "" {
		return nil
	}
	return tea.Println(combined)
}

// commitTurn prints the live viewport's accumulated turn into native
// terminal scrollback and collapses the viewport. Returns nil when the
// viewport is empty. This is the seam that moves finished conversation out
// of the in-memory live region into the terminal's real scrollback, where
// mouse-wheel scrolling and drag-to-copy work and nothing scrolls out of
// reach.
func (m *Model) commitTurn() tea.Cmd {
	if m.fullscreen {
		// Alt-screen mode accumulates the whole transcript in the
		// scrollable viewport (tea.Println is a no-op there), so there is
		// nothing to commit — leave the content in place.
		return nil
	}
	m.finalizeActivePlayback()
	out := strings.TrimRight(m.stream.Commit(), "\n")
	if out == "" {
		return nil
	}
	return tea.Println(out)
}

// finalizeActivePlayback flushes a still-draining previous reply into the
// live viewport and stops the typewriter, so a commit that follows
// captures the WHOLE answer and no stray playbackTick re-commits its tail
// into a later, unrelated scrollback block. No-op when nothing is playing.
//
// playbackActive is only ever set by the agentDoneMsg happy path, where the
// buffered runes ALREADY carry the "● " bullet (prepended at line ~rendered
// assembly). So the drained tail must be appended verbatim — adding another
// assistantBulletPrefix here would inject a second bullet mid-reply.
func (m *Model) finalizeActivePlayback() {
	if !m.playbackActive && len(m.playbackBuf) == 0 {
		return
	}
	if drain := m.drainPlaybackBuffer(); drain != "" {
		m.stream, _ = m.stream.Update(streamWriteMsg(drain))
	}
	m.playbackActive = false
	m.assistantTurnStarted = false
}

// maybePrintIntro prints the intro exactly once per session, regardless of
// whether the splash was dismissed by the timer or a key-skip.
func (m *Model) maybePrintIntro() tea.Cmd {
	if m.introPrinted {
		return nil
	}
	m.introPrinted = true
	return m.printIntro()
}

// printIntro prints the one-time header snapshot + welcome banner into
// native scrollback at the top of the session. The header is a snapshot
// (its live ctx/model/cost readout moved to the pinned footer), so it
// scrolls away with the transcript like the rest of the history.
func (m *Model) printIntro() tea.Cmd {
	intro := strings.TrimRight(m.header.View(), "\n") + "\n\n" +
		strings.TrimRight(m.welcome.View(), "\n")
	if m.fullscreen {
		// Alt-screen: seed the intro into the scrollable viewport (it
		// scrolls with the transcript) since tea.Println does nothing
		// when the alt-screen is active.
		var c tea.Cmd
		m.stream, c = m.stream.Update(streamWriteMsg(intro + "\n\n"))
		return c
	}
	return tea.Println(intro)
}

// applySetupResult drains a setupResult into the TUI: writes the chat
// output, installs the picker if present, kicks off any async command
// (scan / save), and clears the chat on done. The split between
// res.out (synchronous prose) and res.cmd (async work) is preserved
// so the existing scan/save flow keeps its responsive "Scanning…"
// banner while the goroutine runs.
func (m *Model) applySetupResult(res setupResult) tea.Cmd {
	cmds := []tea.Cmd{m.writeStream(res.out)}
	if res.picker != nil {
		m.arrowPicker = res.picker
	}
	if res.cmd != nil {
		cmds = append(cmds, res.cmd)
	}
	if res.done {
		m.setupChat = nil
	}
	return tea.Batch(cmds...)
}

// applyModelSwap is the shared "switch active model to id" path used
// by both `/model <id>` (explicit) and the `/model` picker resolution.
// Calls Deps.SwapModel, updates the cached deps.Model + footer/header,
// and surfaces a confirmation or error line in the stream. Centralising
// keeps the two entry points from drifting (older code had two copies
// of the SwapModel-then-update-footer dance).
func (m *Model) applyModelSwap(id string) tea.Cmd {
	if m.deps.SwapModel != nil {
		if err := m.deps.SwapModel(id); err != nil {
			return m.writeStream(agentError("model", err))
		}
	}
	m.deps.Model = id
	m.footer.SetModel(id)
	var hCmd tea.Cmd
	m.header, hCmd = m.header.Update(headerStateMsg{model: id})
	return tea.Batch(hCmd, m.writeStream(fmt.Sprintf("✓ active model: %s\n", id)))
}

// buildAllModelsPicker materialises the `/model` (no-arg) picker —
// one row per curated model across every provider in loginProviders.
// The hint column shows the provider name + the model's description
// so the operator sees what they're picking without having to know
// the prefix-to-provider mapping. Order matches loginProviders →
// each provider's models in declared order; Anthropic appears first
// because that's the order the registry was built in.
func buildAllModelsPicker() *arrowPicker {
	var items []arrowPickerItem
	for _, p := range loginProviders {
		for _, mdl := range p.models {
			items = append(items, arrowPickerItem{
				label: mdl.id,
				hint:  fmt.Sprintf("%s — %s", p.key, mdl.hint),
				key:   mdl.id,
			})
		}
	}
	return newArrowPicker("Pick a model:", items)
}

// buildSkillsPicker materialises the `/skill` (no-arg) picker —
// one row per skill in the registry, in alphabetical order.
// The hint column shows the skill's description so the operator
// can choose without knowing the exact name in advance.
// Returns nil when the registry is empty.
func buildSkillsPicker(reg *skills.Registry) *arrowPicker {
	all := reg.List()
	if len(all) == 0 {
		return nil
	}
	items := make([]arrowPickerItem, 0, len(all))
	for _, sk := range all {
		items = append(items, arrowPickerItem{
			label: sk.Name,
			hint:  sk.Description,
			key:   sk.Name,
		})
	}
	return newArrowPicker("Pick a skill:", items)
}

// applyLoginResult drains a loginResult into the TUI: writes the chat
// output, activates a picker if present, clears the chat on done, and
// — the part the operator actually cares about — hot-swaps the active
// LLM provider when the chat asks for one. Without the swap, /login
// would save the key to disk but the next question would still hit
// whatever provider was wired at startup (usually Anthropic by default).
func (m *Model) applyLoginResult(res loginResult) tea.Cmd {
	if res.picker != nil {
		m.arrowPicker = res.picker
	}
	if res.done {
		m.loginChat = nil
	}
	// Accumulate chat prose + any swap error into ONE writeStream so they
	// land as a single ordered scrollback block — two separate tea.Println
	// cmds in a Batch are not order-guaranteed.
	out := res.out
	var hCmd tea.Cmd
	if res.swapToModel != "" && m.deps.SwapModel != nil {
		if err := m.deps.SwapModel(res.swapToModel); err != nil {
			out += "\n" + agentError("swap", err)
		} else {
			m.deps.Model = res.swapToModel
			m.footer.SetModel(res.swapToModel)
			m.header, hCmd = m.header.Update(headerStateMsg{model: res.swapToModel})
		}
	}
	return tea.Batch(m.writeStream(out), hCmd)
}

// agentError formats an in-stream diagnostic line for the operator.
// All error surfaces in the TUI (scope, scan, save, agent, …) route
// through this so the bracket convention stays uniform and a future
// localisation pass touches one helper.
func agentError(scope string, err error) string {
	return fmt.Sprintf("[%s: %v]\n", scope, err)
}

// dismissOpenOverlays closes any blocking surface that would swallow
// keystrokes meant for a higher-priority handler (e.g. the approval
// gate, the global Ctrl+C). Both the slash-command palette and the
// arrow picker currently qualify. Idempotent.
func (m *Model) dismissOpenOverlays() {
	if m.palette.Active() {
		m.palette.Close()
	}
	m.arrowPicker = nil
}

// handleEscape resolves Esc from any baseline state and returns the
// operator to a clean prompt. The palette / picker / approval-gate
// branches catch Esc before we get here; this is the catch-all for
// inline conversations and in-flight agent runs, plus a "clear the
// prompt" fallback so a plain Esc never feels inert.
//
// Order matters: cancel the conversation that owns the prompt before
// touching the agent, so a /login or /setup mid-flow ends cleanly
// instead of leaving an orphan chat pointer behind.
func (m *Model) handleEscape() tea.Cmd {
	if m.loginChat != nil {
		res := m.loginChat.Step("cancel")
		m.loginChat = nil
		m.dismissOpenOverlays()
		return m.writeStream(res.out)
	}
	if m.setupChat != nil {
		res := m.setupChat.Step("cancel")
		m.setupChat = nil
		m.dismissOpenOverlays()
		return m.writeStream(res.out)
	}
	if m.running {
		// Same buffer-discard rationale as the Ctrl+C path: Esc means
		// "stop, return me to the prompt", not "keep typing the half
		// of the response I haven't read yet at typewriter pace".
		m.cancel()
		m.cancel = func() {}
		m.running = false
		m.thinking.streaming = false
		m.prompt.SetInFlight(false)
		m.assistantTurnStarted = false
		m.playbackBuf = m.playbackBuf[:0]
		m.playbackEmittable = 0
		m.playbackActive = false
		// Mirror Ctrl+C: preserve the queued question + any in-flight turn
		// content in native scrollback so it doesn't disappear on cancel.
		var cmds []tea.Cmd
		if flush := m.flushPendingUserEcho(); flush != "" {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamWriteMsg(flush))
			cmds = append(cmds, sCmd)
		}
		if c := m.commitTurn(); c != nil {
			cmds = append(cmds, c)
		}
		return tea.Batch(cmds...)
	}
	// Nothing to cancel — clear the prompt so Esc always feels like
	// it did something instead of silently no-op'ing.
	if m.prompt.Value() != "" {
		m.prompt.SetValue("")
	}
	return nil
}

// handlePaletteAction dispatches a palette selection.
func (m *Model) handlePaletteAction(action paletteActionMsg) tea.Cmd {
	switch action.cmd {
	case "tab-complete":
		m.prompt.SetValue("/" + action.arg + " ")
		return nil

	case "clear":
		// Same bullet-anchor reset and playback-buffer drop as the
		// Ctrl+L path so a fresh "● " leads the next response and
		// no half-played reply spills onto the cleared screen.
		// The pending user chip is dropped for the same reason — a
		// deliberate /clear should not leak a queued question.
		m.assistantTurnStarted = false
		m.playbackBuf = m.playbackBuf[:0]
		m.playbackEmittable = 0
		m.playbackActive = false
		m.pendingUserEcho = ""
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamClearMsg{})
		m.prompt.SetValue("")
		// Native scrollback can't be un-printed; clear the visible screen so
		// /clear still reads as a fresh slate (history stays scrollable).
		return tea.Batch(sCmd, tea.ClearScreen)

	case "compact":
		m.prompt.SetValue("")
		if m.running {
			return m.writeStream("⚠ cannot /compact while a turn is in flight; wait for it to finish.\n")
		}
		if m.deps.CompactHistory == nil {
			return m.writeStream("compact unavailable\n")
		}
		return tea.Batch(
			m.writeStream("→ compacting conversation…\n"),
			compactCmd(m.deps.CompactHistory),
		)

	case "new":
		m.prompt.SetValue("")
		if m.running {
			return m.writeStream("⚠ cannot /new while a turn is in flight; wait for it to finish.\n")
		}
		if m.deps.ResetHistory == nil {
			return m.writeStream("new unavailable\n")
		}
		newID, err := m.deps.ResetHistory()
		if err != nil {
			return m.writeStream(agentError("new", err))
		}
		// Wipe the visible screen and the usage gauge alongside the memory
		// reset, mirroring /clear's bullet/playback reset.
		m.assistantTurnStarted = false
		m.playbackBuf = m.playbackBuf[:0]
		m.playbackEmittable = 0
		m.playbackActive = false
		m.pendingUserEcho = ""
		m.usage = usageAccum{}
		var nCmd tea.Cmd
		m.stream, nCmd = m.stream.Update(streamClearMsg{})
		return tea.Batch(nCmd, tea.ClearScreen,
			m.writeStream("✓ new conversation — session "+newID+"\n"))

	case "autocompact":
		m.prompt.SetValue("")
		m.autoCompact = !m.autoCompact
		if m.autoCompact {
			m.autoCompactFails = 0 // fresh start clears any prior pause
			return m.writeStream(fmt.Sprintf("✓ auto-compact ON — will compact automatically past %d%% context.\n", compactAutoThreshold))
		}
		return m.writeStream("✓ auto-compact OFF — compaction is manual (/compact).\n")

	case "plan":
		m.prompt.SetValue("")
		if m.deps.TogglePlan == nil {
			return m.writeStream("plan toggle unavailable\n")
		}
		// Safe to flip mid-turn: a running turn already snapshotted its plan
		// flag, so the change only takes effect on the next question.
		if m.deps.TogglePlan() {
			return m.writeStream("✓ plan-first investigation ON — multi-step questions open with a hypothesis plan.\n")
		}
		return m.writeStream("✓ plan-first investigation OFF — the agent probes directly.\n")

	case "resume":
		m.prompt.SetValue("")
		if m.running {
			return m.writeStream("⚠ cannot /resume while a turn is in flight; wait for it to finish.\n")
		}
		if action.arg == "" {
			return m.writeStream("usage: /resume <session-id>\n")
		}
		if m.deps.SeedHistory == nil {
			return m.writeStream("resume unavailable\n")
		}
		msgs, _, err := session.LoadHistory(action.arg)
		if err != nil {
			return m.writeStream(agentError("resume", err))
		}
		if err := m.deps.SeedHistory(action.arg, msgs); err != nil {
			return m.writeStream(agentError("resume", err))
		}
		// The resumed context replaces the current one; reset the gauge so
		// it isn't stale until the next turn's usage event recomputes it.
		m.usage.LastInputTokens = 0
		return m.writeStream(fmt.Sprintf("✓ resumed session %s — %d message(s) restored\n", action.arg, len(msgs)))

	case "quit", "exit":
		return tea.Quit

	case "update":
		// Kick the self-update in a tea.Cmd goroutine. While the
		// download runs (~5–15s) the TUI stays responsive; the final
		// selfUpdateDoneMsg dumps the captured progress log plus the
		// outcome into the stream. We surface a short "starting…"
		// line synchronously so the operator gets immediate feedback.
		return tea.Batch(
			m.writeStream("→ checking for cloudy update…\n"),
			selfUpdateCmd(),
		)

	case "help":
		return m.writeStream(helpText())

	case "version":
		return m.writeStream("cloudy " + buildinfo.Version + "\n")

	case "skill":
		m.prompt.SetValue("")
		if action.arg == "" {
			// No name → open an interactive picker listing all available
			// skills, mirroring the bare `/model` flow. The operator
			// arrows + Enters; resolve routes back through
			// handlePaletteAction{cmd:"skill", arg:<name>} via the
			// arrowPickerResolveMsg handler. `/skill <name>` still works
			// for power-users / scripts.
			if m.deps.Skills == nil {
				return m.writeStream("no skills loaded\n")
			}
			picker := buildSkillsPicker(m.deps.Skills)
			if picker == nil {
				return m.writeStream("no skills available\n")
			}
			m.skillPickerActive = true
			m.arrowPicker = picker
			return m.writeStream("\nPick a skill:\n")
		}
		if m.deps.Skills != nil {
			if sk, ok := m.deps.Skills.Get(action.arg); ok {
				m.activeSkill = sk.Name
				var hCmd tea.Cmd
				m.header, hCmd = m.header.Update(headerStateMsg{skill: sk.Name})
				return hCmd
			}
		}
		return m.writeStream("skill not found: " + action.arg + "\n")

	case "use":
		if action.arg == "" {
			return m.writeStream("usage: /use <context>\n")
		}
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{ctx: action.arg})
		m.prompt.SetValue("")
		return hCmd

	case "model":
		m.prompt.SetValue("")
		if action.arg == "" {
			// No id → open the cross-provider picker, mirroring /login
			// step 3 but spanning every curated provider. The operator
			// arrows + Enters; resolve routes to applyModelSwap via the
			// arrowPickerResolveMsg handler. /model <id> still works for
			// power-users / scripts.
			m.modelPickerActive = true
			m.arrowPicker = buildAllModelsPicker()
			return m.writeStream("\nPick a model (any provider you have a key for):\n")
		}
		return m.applyModelSwap(action.arg)

	case "scope":
		m.prompt.SetValue("")
		if action.arg == "" {
			// Bare /scope — kick off an async kubectl get ns and let
			// the operator pick from the live namespace list via a
			// multi-select arrow picker. This mirrors how /model
			// (no-arg) and /setup (multi-select) handle HITL flows.
			m.scopePickerActive = true
			return tea.Batch(
				m.writeStream("→ fetching namespaces…\n"),
				fetchScopeNamespacesCmd(m.deps.InitialCtx),
			)
		}
		return m.handleScopeCmd(action.arg)

	case "tools":
		return m.writeStream(renderInventory(m.deps.Tools))

	case "replay":
		return m.writeStream("replay not yet implemented\n")

	case "setup", "set-up":
		m.prompt.SetValue("")
		return m.enterSetup()

	case "login":
		m.prompt.SetValue("")
		chat, res := newLoginChat()
		m.loginChat = chat
		if res.picker != nil {
			m.arrowPicker = res.picker
		}
		return m.writeStream(res.out)
	}

	m.prompt.SetValue("")
	return nil
}

// enterSetup starts the stream-inline /setup conversation. The full-screen
// wizard (legacy modeSetup branch) is gone; the CLI `cloudy setup` entry in
// cmd/main.go still drives setup.Run directly for non-interactive bootstrap.
//
// The discovery scan respects internal/discovery's own 30 s deadline, so the
// chat does not need to hold a cancellable context — plain Background is
// enough and avoids the leak that the deleted exitSetup helper used to clean
// up.
func (m *Model) enterSetup() tea.Cmd {
	chat, res := newSetupChat(context.Background(), "", config.Path(), config.ProfilePath())
	m.prompt.SetValue("")
	if chat == nil {
		// No kubeconfig contexts found: result carries the error string;
		// nothing else to install.
		return m.writeStream(res.out)
	}
	m.setupChat = chat
	return m.applySetupResult(res)
}

// handleScopeCmd parses and applies a /scope argument, emitting confirmation.
func (m *Model) handleScopeCmd(arg string) tea.Cmd {
	sc, err := parseScope(arg)
	if err != nil {
		return m.writeStream(agentError("scope error", err))
	}

	m.scope = sc

	var scopeStr string
	if sc.Empty() {
		scopeStr = "-" // sentinel to clear in headerStateMsg
	} else {
		scopeStr = sc.String()
	}

	var cmds []tea.Cmd
	var hCmd tea.Cmd
	m.header, hCmd = m.header.Update(headerStateMsg{scope: scopeStr})
	cmds = append(cmds, hCmd)

	feedback := "[scope reset]\n"
	if !sc.Empty() {
		feedback = "[scope set: " + sc.String() + "]\n"
	}
	cmds = append(cmds, m.writeStream(feedback))

	m.prompt.SetValue("")
	return tea.Batch(cmds...)
}

func helpText() string {
	return `cloudy TUI — keyboard shortcuts

  Enter          submit prompt
  Shift+Enter    insert newline
  Up / Down      navigate history (or palette when open)
  Ctrl+R         incremental history search
  Ctrl+L         clear the visible screen (scrollback history is preserved)
  Ctrl+C         cancel in-flight request (works even when palette is open)
  Ctrl+C×2       quit cloudy
  Esc            cancel request / close palette
  Tab            open command palette / tab-complete selected command
  Mouse wheel    scroll the conversation (your terminal's native scrollback)
  PageUp/Dn      scroll the live reply (use mouse wheel / terminal for history)

commands (type / to open suggestions; arrow keys to pick):
  /setup              run the discovery wizard
  /login              save an LLM provider API key inline
  /skill <name>       switch active skill
  /use <ctx>          switch kubeconfig context
  /model <id>         switch model
  /scope ns=<csv>     narrow session to namespaces (comma-separated)
  /scope ctx=<csv>    narrow session to contexts (comma-separated)
  /scope reset        drop session scope
  /tools              list registered tool groups + skip reasons
  /replay <session>   replay session
  /clear              clear the visible screen (scrollback is preserved)
  /update             upgrade cloudy to the latest GitHub release
  /help               show this text
  /version            print version
  /quit               exit cloudy (alias: /exit)
`
}
