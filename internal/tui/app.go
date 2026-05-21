package tui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rlaope/cloudy/internal/buildinfo"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
)

// ctrlCTimeout is the window within which two Ctrl+C presses quit the program.
const ctrlCTimeout = 2 * time.Second

// splashDuration is how long the boot splash (cloudy banner + animated dots)
// stays on screen before the main TUI takes over. Long enough to make the
// brand land, short enough that the operator never has to wait on it.
// The first KeyMsg also dismisses it early so eager typists never feel
// the brand getting in their way.
const splashDuration = 350 * time.Millisecond

// splashTickInterval drives the dots animation while the splash is visible.
const splashTickInterval = 90 * time.Millisecond

// splashTickMsg fires once per splashTickInterval until splashDuration elapses.
type splashTickMsg struct{}

// splashTickCmd returns a tea.Cmd that emits one splashTickMsg.
func splashTickCmd() tea.Cmd {
	return tea.Tick(splashTickInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
}

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
}

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
}

// NewModel constructs the root TUI model.
func NewModel(deps Deps) Model {
	keys := defaultKeys()
	noColor := false

	state := footerStateReady
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
		m.splash.frame++
		if time.Since(m.splash.start) >= splashDuration {
			m.splash.done = true
			// Seed the stream with the welcome banner so it scrolls up
			// naturally as the operator starts chatting instead of
			// disappearing on the first input. Synchronous write so
			// the banner is visible the moment the splash dismisses.
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamWriteMsg(m.welcome.View() + "\n\n"))
			return m, sCmd
		}
		return m, splashTickCmd()

	case thinkingTickMsg:
		if !m.running {
			// Agent finished or was cancelled; let the tick loop die.
			return m, nil
		}
		m.thinking.tick()
		return m, thinkingTickCmd()

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
			m.stream, _ = m.stream.Update(streamWriteMsg(m.welcome.View() + "\n\n"))
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
			// Single Ctrl+C: cancel in-flight request.
			m.cancel()
			m.cancel = func() {}
			m.running = false
			m.thinking.streaming = false
			m.prompt.SetInFlight(false)
			m.assistantTurnStarted = false
			return m, nil

		case "ctrl+l":
			// Reset the assistant-turn anchor so the next agent token
			// after a clear gets a fresh "● " bullet. Otherwise the
			// post-clear reply materialises with no visual start.
			m.assistantTurnStarted = false
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamClearMsg{})
			return m, sCmd

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

		// Echo the user's question into the stream so it scrolls up
		// alongside the agent's answer; otherwise the operator only
		// sees the response and loses track of what was asked. Use
		// streamWriteMsg so the echo lands immediately rather than
		// waiting for the next agent-token flush tick.
		echo := userEchoStyle.Render("> "+val) + "\n"
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamWriteMsg("\n" + echo))

		// Setup gate: refuse to dispatch when no model has been picked.
		// We deliberately don't gate on m.deps.Provider — that field is a
		// stale snapshot from startup. The live provider lives inside the
		// agent runner's providerRef, swapped via /login or /model; m.deps.Model
		// is the only field updated by those swaps, so it's the truth here.
		if m.deps.Model == "" {
			warn := setupRequiredStyle.Render(
				"⚠ no LLM model selected. Run /login to pick a provider, paste "+
					"an API key, and choose a model — chat works without /setup; "+
					"/setup only adds read-only infrastructure probes (k8s, prom, "+
					"loki, …) so cloudy can investigate questions about your clusters.",
			) + "\n"
			var wCmd tea.Cmd
			m.stream, wCmd = m.stream.Update(streamWriteMsg(warn))
			return m, tea.Batch(sCmd, wCmd)
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

		return m, tea.Batch(sCmd, m.runAgent(val), thinkingTickCmd())

	case arrowPickerResolveMsg:
		// Single-select picker — could be /login (provider OR model step),
		// or the standalone /model picker. Route by which mode is active.
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
		return m, nil

	case arrowPickerMultiResolveMsg:
		// Multi-select picker (e.g. /setup contexts + backend kinds).
		// Only setupChat consumes these today; other chats fall through.
		m.arrowPicker = nil
		if m.setupChat != nil {
			res := m.setupChat.ApplyMulti(msg.keys, msg.cancelled)
			return m, m.applySetupResult(res)
		}
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
		// Save succeeded: flip the footer state segment and reset the
		// welcome banner so the next launch / cleared stream shows the
		// returning-user form. Used to live in exitSetup; now lives on
		// the actual success edge.
		if msg.err == nil {
			m.footer.SetState(footerStateReady)
			m.welcome = NewWelcomeModel(false, m.deps.InitialCtx)
			m.welcome.SetWidth(m.width)
		}
		return m, m.writeStream(res.out)

	case agentDoneMsg:
		m.running = false
		m.thinking.streaming = false
		m.prompt.SetInFlight(false)
		// Reset so the next turn re-emits the "● " bullet on its first
		// token. Without this the second turn's response would inline
		// against the previous one with no visual break.
		m.assistantTurnStarted = false
		// Release the channel so a stray late pump-cmd from the previous
		// event doesn't keep this goroutine alive or block on a closed
		// channel. Subsequent runAgent calls install a fresh ch.
		m.agentCh = nil
		if msg.err != nil {
			return m, m.writeStream("\n" + agentError("error", msg.err))
		}
		return m, nil

	case agentUsageMsg:
		m.usage.Input += msg.Input
		m.usage.Output += msg.Output
		m.usage.USD += msg.USD
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

	header := m.header.View()
	prompt := m.prompt.View()
	paletteView := m.palette.View()
	footer := m.footer.View()
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

	// Compute the body height by subtracting every other component's actual
	// rendered height from the terminal height. lipgloss.Height counts rows
	// correctly even when content wraps, so this stays correct in narrow
	// split panes.
	//
	// chromeBottomPad reserves two extra rows below the footer: one as
	// breathing room between the prompt and the footer, one as bottom
	// padding so the TUI doesn't sit flush against the terminal edge.
	const chromeBottomPad = 2
	headerH := lipgloss.Height(header)
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
	bodyH := m.height - headerH - promptH - paletteH - footerH - bannerH - thinkingH - pickerH - chromeBottomPad
	if bodyH < 1 {
		bodyH = 1
	}
	m.stream.SetViewportSize(m.width, bodyH)

	body := m.stream.View()
	if m.stream.Empty() {
		body = m.welcome.View() + "\n" + body
	}

	// Composed bottom-up: header → body (stream/welcome) → optional
	// approval banner → optional thinking row → prompt → optional palette
	// suggestions → blank separator → status footer → blank bottom
	// padding. The trailing blank lifts the footer off the terminal edge
	// so the chrome doesn't feel cramped.
	parts := []string{header, body}
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
	parts = append(parts, prompt)
	if paletteView != "" {
		parts = append(parts, paletteView)
	}
	parts = append(parts, "", footer, "")

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
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

// applyAgentEvent routes a single agent event to the appropriate sub-model.
func (m *Model) applyAgentEvent(evt AgentEvent) tea.Cmd {
	var cmds []tea.Cmd

	if evt.Token != "" {
		// Switch the thinking row from verb-cycling to "Streaming" once
		// real bytes arrive. tokens is a coarse char-rate stand-in
		// until evt.Usage from the provider gives us a true token count.
		m.thinking.streaming = true
		m.thinking.tokens += approxTokens(evt.Token)
		token := evt.Token
		if !m.assistantTurnStarted {
			// Anchor the response with a styled bullet — same affordance
			// Claude's CLI uses — so the first token never looks like it
			// materialised mid-air against the user echo. Prepending here
			// (rather than as a separate message) keeps the prefix in
			// the same batched flush as the first chunk, so the bullet
			// and the first words appear together rather than flickering.
			token = "\n" + assistantPrefixStyle.Render("●") + " " + token
			m.assistantTurnStarted = true
		}
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg(token))
		cmds = append(cmds, sCmd)
	}
	if evt.ToolBegin != nil {
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

// writeStream is the every-other-palette-action shape: clear the prompt
// and append text to the stream output. Extracted because the same three
// lines appeared in 11 branches of handlePaletteAction; collapsing them
// makes the dispatcher scannable.
//
// Uses streamWriteMsg so the chrome line lands synchronously instead of
// going through the agent-token batching path — chat diagnostics need
// to be visible on the same Update tick they were issued on.
func (m *Model) writeStream(s string) tea.Cmd {
	var c tea.Cmd
	m.stream, c = m.stream.Update(streamWriteMsg(s))
	m.prompt.SetValue("")
	return c
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

// applyLoginResult drains a loginResult into the TUI: writes the chat
// output, activates a picker if present, clears the chat on done, and
// — the part the operator actually cares about — hot-swaps the active
// LLM provider when the chat asks for one. Without the swap, /login
// would save the key to disk but the next question would still hit
// whatever provider was wired at startup (usually Anthropic by default).
func (m *Model) applyLoginResult(res loginResult) tea.Cmd {
	cmds := []tea.Cmd{m.writeStream(res.out)}
	if res.picker != nil {
		m.arrowPicker = res.picker
	}
	if res.done {
		m.loginChat = nil
	}
	if res.swapToModel != "" && m.deps.SwapModel != nil {
		if err := m.deps.SwapModel(res.swapToModel); err != nil {
			cmds = append(cmds, m.writeStream(agentError("swap", err)))
		} else {
			m.deps.Model = res.swapToModel
			m.footer.SetModel(res.swapToModel)
			var hCmd tea.Cmd
			m.header, hCmd = m.header.Update(headerStateMsg{model: res.swapToModel})
			cmds = append(cmds, hCmd)
		}
	}
	return tea.Batch(cmds...)
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
		m.cancel()
		m.cancel = func() {}
		m.running = false
		m.thinking.streaming = false
		m.prompt.SetInFlight(false)
		m.assistantTurnStarted = false
		return nil
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
		// Same bullet-anchor reset as the Ctrl+L path so a fresh
		// "● " leads the next response.
		m.assistantTurnStarted = false
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamClearMsg{})
		m.prompt.SetValue("")
		return sCmd

	case "quit", "exit":
		return tea.Quit

	case "update":
		return m.writeStream(renderUpdateInstructions())

	case "help":
		return m.writeStream(helpText())

	case "version":
		return m.writeStream("cloudy " + buildinfo.Version + "\n")

	case "skill":
		if action.arg == "" {
			return m.writeStream("usage: /skill <name>\n")
		}
		if m.deps.Skills != nil {
			if sk, ok := m.deps.Skills.Get(action.arg); ok {
				m.activeSkill = sk.Name
				var hCmd tea.Cmd
				m.header, hCmd = m.header.Update(headerStateMsg{skill: sk.Name})
				m.prompt.SetValue("")
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
		// Insert prefix into prompt so user fills in the argument.
		m.prompt.SetValue("/scope ")
		return nil

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

// splashDotsStyle is the lighter sky-blue colour used by the splash
// trailer. Kept at package scope so the splash tick (120ms) does not
// rebuild the style on every frame.
var splashDotsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("153"))

// approxTokens estimates the token count of a streaming chunk using
// the four-chars-per-token rule of thumb. Used until the provider
// emits an authoritative Usage event with the real output count.
func approxTokens(s string) int {
	n := len(s) / 4
	if n < 1 && s != "" {
		return 1
	}
	return n
}

// formatThinkingElapsed returns "3s" / "1m05s" / "1h05m" for the
// thinking row's compact timer.
func formatThinkingElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s/60)%60)
	}
}

// renderThinkingRow returns the row that sits directly above the prompt.
// It is rendered unconditionally from app start so the prompt position
// never jumps when an agent run begins or ends — earlier versions
// returned "" while idle, which made every Enter and every agentDoneMsg
// shove the prompt up/down by a row. Three states:
//
//	· ready                                  -- idle, between turns
//	✦ Synthesizing… (3s · 240 tokens)        -- thinking, no bytes yet
//	✦ Streaming    (1m12s · 1240 tokens)     -- bytes arriving
func (m Model) renderThinkingRow() string {
	if !m.running {
		return thinkingIdleStyle.Render("· ready")
	}
	elapsed := formatThinkingElapsed(time.Since(m.thinking.start))
	verb := "Streaming"
	if !m.thinking.streaming {
		verb = thinkingVerbs[m.thinking.verbIdx] + "…"
	}
	line := fmt.Sprintf("✦ %s   (%s · %d tokens)", verb, elapsed, m.thinking.tokens)
	return thinkingStyle.Render(line)
}

// userEchoStyle renders the "> <input>" line that mirrors the operator's
// last submitted prompt back into the stream. The previous "chip" form
// (bold white-on-dark-grey with padding) read as a UI badge; the
// chevron-led plain line reads as a transcript turn, matching how
// Claude's CLI presents prior questions and clearly distinguishable
// from the styled "●" the agent reply now leads with.
var userEchoStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("250"))

// setupRequiredStyle is the red banner shown in-stream when the operator
// asks a question before /setup or /login has configured a model.
var setupRequiredStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("196")).Bold(true)

// thinkingStyle is the soft sky-blue used by the in-flight agent status
// row ("✦ Synthesizing… (3s · 240 tokens)") that sits above the prompt.
var thinkingStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("153"))

// thinkingIdleStyle is the dim grey used by the persistent "· ready"
// row that holds the layout slot while no agent run is in flight. The
// muted shade keeps the eye on the prompt where input belongs.
var thinkingIdleStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240"))

// assistantPrefixStyle styles the "●" bullet that anchors every agent
// response. Same brand sky-blue as the welcome banner so the cue feels
// of-a-piece with the rest of cloudy's chrome instead of an ad-hoc
// glyph dropped in front of the text.
var assistantPrefixStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("117")).Bold(true)

// approvalBannerStyle paints the first row of the RiskHigh approval
// banner. White on red, bold — impossible to scroll past while the
// agent goroutine waits on a y/n decision. The previous plain-text
// banner blended into the transcript during a fast tool sequence and
// the operator could miss that the agent had paused.
var approvalBannerStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("15")).
	Background(lipgloss.Color("196")).
	Bold(true).
	Padding(0, 1)

// approvalHintStyle is the muted second line ("press [y] / [n] / Esc").
// Lower contrast on purpose so the eye lands on the warning line first
// and treats the hint as supporting context.
var approvalHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("250"))

// thinkingVerbs cycle as the agent works so the screen never looks
// frozen during a long generation. Trimmed from the original nine-verb
// pool (which included "Catapulting" / "Spelunking" — fun once, off-
// brand on a tenth incident response) to four neutral, evergreen verbs
// that read as serious-but-alive.
var thinkingVerbs = []string{
	"Thinking",
	"Working",
	"Synthesizing",
	"Pondering",
}

// thinkingTickInterval and thinkingVerbRotateTicks together pace the
// in-flight animation: 250ms per tick, verb changes every 8 ticks (≈2s).
const (
	thinkingTickInterval    = 250 * time.Millisecond
	thinkingVerbRotateTicks = 8
)

// thinkingTickMsg fires once per thinkingTickInterval while an agent run
// is active, prompting View() to refresh the elapsed counter.
type thinkingTickMsg struct{}

// thinkingTickCmd returns a tea.Cmd that emits one thinkingTickMsg.
func thinkingTickCmd() tea.Cmd {
	return tea.Tick(thinkingTickInterval, func(time.Time) tea.Msg { return thinkingTickMsg{} })
}

// thinkingState bundles every field that drives the in-flight agent
// row. The fields always mutate together (set in submitMsg, cleared in
// agentDoneMsg, read in renderThinkingRow) — keeping them on one
// struct makes the lifetime explicit and lets the helpers replace
// 5-line reset/tick sequences with a single call.
type thinkingState struct {
	start     time.Time
	tokens    int
	verbIdx   int
	streaming bool
	tickCount int
}

// reset begins a new in-flight run. Verb index is seeded from the
// nanosecond clock so consecutive runs feel different.
func (t *thinkingState) reset() {
	*t = thinkingState{
		start:   time.Now(),
		verbIdx: int(time.Now().UnixNano()) % len(thinkingVerbs),
	}
	if t.verbIdx < 0 {
		t.verbIdx += len(thinkingVerbs)
	}
}

// tick advances the rotating-verb animation.
func (t *thinkingState) tick() {
	t.tickCount++
	if t.tickCount%thinkingVerbRotateTicks == 0 {
		t.verbIdx = (t.verbIdx + 1) % len(thinkingVerbs)
	}
}

// splashState bundles the brand-banner gate. done flips once the
// splashDuration has elapsed; frame drives the dots animation.
type splashState struct {
	start time.Time
	done  bool
	frame int
}

// renderSplash returns the boot splash frame: the welcome banner with an
// animated "initialising…" trailer driven by splash.frame. Padded with a
// blank line so the body lands roughly mid-screen on a typical terminal.
func (m Model) renderSplash() string {
	banner := m.welcome.View()
	dots := strings.Repeat(".", (m.splash.frame%3)+1)
	trailer := "  " + splashDotsStyle.Render("initialising"+dots)
	return banner + "\n\n" + trailer
}

// renderUpdateInstructions returns a self-update guide rather than running
// the installer from inside the agent process: a long-running binary cannot
// safely overwrite itself on disk, and silently triggering a download while
// the user is mid-session would be surprising. Print the one-liner instead
// and let the operator decide when to apply it.
func renderUpdateInstructions() string {
	var b strings.Builder
	b.WriteString("\n--- cloudy update ---\n")
	fmt.Fprintf(&b, "  current : %s  (%s/%s)\n", buildinfo.Version, runtime.GOOS, runtime.GOARCH)
	b.WriteString("  latest  : https://github.com/rlaope/cloudy/releases/latest\n\n")
	b.WriteString("Exit cloudy first (Ctrl+C twice or /exit), then run:\n\n")
	b.WriteString("  curl -fsSL https://raw.githubusercontent.com/rlaope/cloudy/master/install.sh | sh\n\n")
	b.WriteString("The installer pulls whatever GitHub marks as `latest`, drops the\n")
	b.WriteString("binary in ~/.local/bin/cloudy (or $CLOUDY_INSTALL_DIR), and prints a\n")
	b.WriteString("PATH-setup hint if needed. Re-run the same line later to upgrade.\n\n")
	b.WriteString("Contributors / off-matrix platforms can still build from source:\n\n")
	b.WriteString("  cd <your cloudy clone>\n")
	b.WriteString("  git pull && make build\n")
	return b.String()
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
  Ctrl+L         clear stream
  Ctrl+C         cancel in-flight request (works even when palette is open)
  Ctrl+C×2       quit cloudy
  Esc            cancel request / close palette
  Tab            open command palette / tab-complete selected command
  PageUp/Dn      scroll output

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
  /clear              clear output
  /update             show install commands for the latest cloudy release
  /help               show this text
  /version            print version
  /quit               exit cloudy (alias: /exit)
`
}
