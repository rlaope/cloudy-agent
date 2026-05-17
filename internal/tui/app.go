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
const splashDuration = 700 * time.Millisecond

// splashTickInterval drives the dots animation while the splash is visible.
const splashTickInterval = 120 * time.Millisecond

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
			// disappearing on the first input.
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamTokenMsg(m.welcome.View() + "\n\n"))
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
			case "enter":
				sel := m.arrowPicker.Selected()
				return m, arrowPickerResolveCmd(sel.key, false)
			case "esc":
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
			return m, nil

		case "ctrl+l":
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
			m.cancel()
			m.cancel = func() {}
			m.running = false
			return m, nil

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
			if res.done {
				m.loginChat = nil
			}
			return m, m.writeStream(res.out)
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
		// sees the response and loses track of what was asked.
		echo := userEchoStyle.Render("> "+val) + "\n"
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("\n" + echo))

		// Setup gate: refuse to dispatch when no provider/model is
		// configured. The agent goroutine would otherwise silently
		// finish with no output, leaving the operator confused.
		if m.deps.Provider == nil || m.deps.Model == "" {
			warn := setupRequiredStyle.Render(
				"⚠ cloudy is not configured. Run /setup to discover your clusters "+
					"and pick a model, or /login to save an API key.",
			) + "\n"
			var wCmd tea.Cmd
			m.stream, wCmd = m.stream.Update(streamTokenMsg(warn))
			return m, tea.Batch(sCmd, wCmd)
		}

		// Start the in-flight thinking animation. Reset seeds a new
		// verb and zeroes the streaming counters.
		m.thinking.reset()

		return m, tea.Batch(sCmd, m.runAgent(val), thinkingTickCmd())

	case arrowPickerResolveMsg:
		// Picker has fired its decision. Route the answer to whichever
		// inline chat is active; if none, just drop the picker.
		m.arrowPicker = nil
		input := msg.key
		if msg.cancelled {
			input = "cancel"
		}
		if m.loginChat != nil {
			res := m.loginChat.Step(input)
			if res.done {
				m.loginChat = nil
			}
			if res.picker != nil {
				m.arrowPicker = res.picker
			}
			return m, m.writeStream(res.out)
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
		evt := AgentEvent(msg)
		return m, m.applyAgentEvent(evt)

	case setupScanDoneMsg:
		if m.setupChat == nil {
			return m, nil
		}
		res := m.setupChat.Apply(msg)
		if res.done {
			m.setupChat = nil
		}
		cmds := []tea.Cmd{m.writeStream(res.out)}
		if res.cmd != nil {
			cmds = append(cmds, res.cmd)
		}
		return m, tea.Batch(cmds...)

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
		banner = fmt.Sprintf("⚠ approval required: %s(%s) — RiskHigh\n  press [y] to approve, [n] or Esc to deny",
			m.pendingApproval.Tool, m.pendingApproval.Args)
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
	thinkingH := 0
	if thinking != "" {
		thinkingH = lipgloss.Height(thinking)
	}
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
	if thinking != "" {
		parts = append(parts, thinking)
	}
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
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg(evt.Token))
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
func (m *Model) writeStream(s string) tea.Cmd {
	var c tea.Cmd
	m.stream, c = m.stream.Update(streamTokenMsg(s))
	m.prompt.SetValue("")
	return c
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

// handlePaletteAction dispatches a palette selection.
func (m *Model) handlePaletteAction(action paletteActionMsg) tea.Cmd {
	switch action.cmd {
	case "tab-complete":
		m.prompt.SetValue("/" + action.arg + " ")
		return nil

	case "clear":
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
		if action.arg == "" {
			return m.writeStream("usage: /model <id>\n")
		}
		m.deps.Model = action.arg
		m.footer.SetModel(action.arg)
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{model: action.arg})
		m.prompt.SetValue("")
		return hCmd

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
	chat, greeting := newSetupChat(context.Background(), "", config.Path(), config.ProfilePath())
	m.prompt.SetValue("")
	if chat == nil {
		// No kubeconfig contexts found: the greeting is the error message;
		// just write it without installing the conversation.
		return m.writeStream(greeting)
	}
	m.setupChat = chat
	return m.writeStream(greeting)
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

// renderThinkingRow returns the in-flight agent status row, or an
// empty string when no run is active. Format:
//
//	✦ Synthesizing… (3s · 240 tokens)
//	✦ Streaming   (1m12s · 1240 tokens)
func (m Model) renderThinkingRow() string {
	if !m.running {
		return ""
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
// last submitted prompt back into the stream. Bright-white text on a
// dark-grey background with a single-cell horizontal padding so the
// echo reads as a distinct chip — matches Claude's input-replay
// affordance and makes it impossible to confuse with agent output.
var userEchoStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("15")).
	Background(lipgloss.Color("236")).
	Bold(true).
	Padding(0, 1)

// setupRequiredStyle is the red banner shown in-stream when the operator
// asks a question before /setup or /login has configured a model.
var setupRequiredStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("196")).Bold(true)

// thinkingStyle is the soft sky-blue used by the in-flight agent status
// row ("✦ Synthesizing… (3s · 240 tokens)") that sits above the prompt.
var thinkingStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("153"))

// thinkingVerbs cycle as the agent works so the screen never looks
// frozen during a long generation. Phrasing borrows from Claude's CLI
// for familiarity; one is picked at random per run and rotated every
// thinkingVerbRotateTicks ticks.
var thinkingVerbs = []string{
	"Thinking",
	"Hmm",
	"Cogitating",
	"Pondering",
	"Synthesizing",
	"Catapulting",
	"Spelunking",
	"Brewing",
	"Mulling",
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

// renderUpdateInstructions returns a self-update guide rather than running git
// or make from inside the agent process: a long-running binary cannot safely
// overwrite itself on disk, and silently triggering a rebuild while the user
// is mid-session would be surprising. Print the commands instead and let the
// operator decide when to apply them.
func renderUpdateInstructions() string {
	var b strings.Builder
	b.WriteString("\n--- cloudy update ---\n")
	fmt.Fprintf(&b, "  current : %s  (%s/%s)\n", buildinfo.Version, runtime.GOOS, runtime.GOARCH)
	b.WriteString("  latest  : https://github.com/rlaope/cloudy/releases/latest\n\n")
	b.WriteString("Run from your cloudy clone (do NOT edit the running binary):\n\n")
	b.WriteString("  git fetch --tags origin\n")
	b.WriteString("  git checkout v0.4.0          # or the tag from the link above\n")
	b.WriteString("  make build\n")
	b.WriteString("  ./cloudy --version\n\n")
	b.WriteString("If cloudy is on $PATH via a symlink (e.g. /usr/local/bin/cloudy → repo)\n")
	b.WriteString("the rebuild propagates automatically; otherwise copy the new binary into\n")
	b.WriteString("place after exit. Use /exit to leave this session first.\n")
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
  /setup              run the discovery wizard (alias: /set-up)
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
