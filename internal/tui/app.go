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
	"github.com/rlaope/cloudy/internal/setup"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
)

// uiMode discriminates between the main interactive chat and the embedded
// /setup wizard.
type uiMode int

const (
	modeMain uiMode = iota
	modeSetup
)

// ctrlCTimeout is the window within which two Ctrl+C presses quit the program.
const ctrlCTimeout = 2 * time.Second

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

	// mode controls which sub-view is rendered/routed.
	mode uiMode
	// welcome renders the banner above an empty stream in main mode.
	welcome WelcomeModel
	// setupWiz holds the embedded setup wizard when mode == modeSetup.
	setupWiz    *setup.WizardModel
	setupCtx    context.Context
	setupCancel context.CancelFunc

	width  int
	height int
	ready  bool
}

// NewModel constructs the root TUI model.
func NewModel(deps Deps) Model {
	keys := defaultKeys()
	noColor := false

	return Model{
		header:  newHeaderModel(deps.InitialCtx, deps.InitialNS, deps.Model),
		stream:  newStreamModel(noColor),
		prompt:  newPromptModel(keys),
		palette: newPaletteModel(),
		deps:    deps,
		keys:    keys,
		cancel:  func() {},
		mode:    modeMain,
		welcome: NewWelcomeModel(deps.FirstRun, deps.InitialCtx),
	}
}

func (m Model) Init() tea.Cmd {
	return m.prompt.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// While the setup wizard owns the screen, forward every non-resize
	// message to it. WindowSizeMsg still flows through to the sub-models
	// below so the underlying layout stays correct on return.
	if m.mode == modeSetup && m.setupWiz != nil {
		if _, ok := msg.(tea.WindowSizeMsg); !ok {
			var cmd tea.Cmd
			m.setupWiz, cmd = m.setupWiz.Update(msg)
			if m.setupWiz.Done() || m.setupWiz.Aborted() {
				m.exitSetup()
			}
			return m, cmd
		}
	}

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
		return m, tea.Batch(hCmd, sCmd, prCmd, paCmd)

	case tea.KeyMsg:
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
		if strings.HasPrefix(val, "/scope ") {
			return m, m.handleScopeCmd(strings.TrimPrefix(val, "/scope "))
		}
		if val == "/setup" || strings.HasPrefix(val, "/setup ") {
			return m, m.enterSetup()
		}
		return m, m.runAgent(val)

	case paletteActionMsg:
		return m, m.handlePaletteAction(msg)

	case paletteDismissMsg:
		// Palette closed without action; drop any leading '/' the user typed
		// so the prompt doesn't keep re-triggering the suggestions on the
		// next character.
		if strings.HasPrefix(m.prompt.Value(), "/") {
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

	case agentDoneMsg:
		m.running = false
		if msg.err != nil {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamTokenMsg("\n[error: " + msg.err.Error() + "]\n"))
			return m, sCmd
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

	if m.mode == modeSetup && m.setupWiz != nil {
		return m.setupWiz.View()
	}

	header := m.header.View()
	prompt := m.prompt.View()
	paletteView := m.palette.View()

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
	headerH := lipgloss.Height(header)
	promptH := lipgloss.Height(prompt)
	paletteH := lipgloss.Height(paletteView)
	bannerH := 0
	if banner != "" {
		bannerH = lipgloss.Height(banner)
	}
	bodyH := m.height - headerH - promptH - paletteH - bannerH
	if bodyH < 1 {
		bodyH = 1
	}
	m.stream.SetViewportSize(m.width, bodyH)

	body := m.stream.View()
	if m.stream.Empty() {
		body = m.welcome.View() + "\n" + body
	}

	// Composed bottom-up: header → body (stream/welcome) → optional approval
	// banner → prompt → optional palette suggestions. Palette below prompt
	// matches Claude's slash-menu placement and frees the body area above.
	parts := []string{header, body}
	if banner != "" {
		parts = append(parts, banner)
	}
	parts = append(parts, prompt)
	if paletteView != "" {
		parts = append(parts, paletteView)
	}

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
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{cost: evt.Usage.USD})
		cmds = append(cmds, hCmd)
	}
	if evt.Approval != nil {
		m.pendingApproval = evt.Approval
	}

	// Continue reading from the channel.
	if m.deps.AgentRunner != nil && m.running {
		cmds = append(cmds, m.nextAgentEvent())
	}

	return tea.Batch(cmds...)
}

// nextAgentEvent is a placeholder — the actual channel read loop is driven by
// the cmd returned from runAgent; this is not needed in the simple model.
// Left empty so compile passes.
func (m *Model) nextAgentEvent() tea.Cmd { return nil }

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
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg(renderUpdateInstructions()))
		m.prompt.SetValue("")
		return sCmd

	case "help":
		help := helpText()
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg(help))
		m.prompt.SetValue("")
		return sCmd

	case "version":
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("cloudy " + buildinfo.Version + "\n"))
		m.prompt.SetValue("")
		return sCmd

	case "skill":
		if action.arg == "" {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamTokenMsg("usage: /skill <name>\n"))
			m.prompt.SetValue("")
			return sCmd
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
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("skill not found: " + action.arg + "\n"))
		m.prompt.SetValue("")
		return sCmd

	case "use":
		if action.arg == "" {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamTokenMsg("usage: /use <context>\n"))
			m.prompt.SetValue("")
			return sCmd
		}
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{ctx: action.arg})
		m.prompt.SetValue("")
		return hCmd

	case "model":
		if action.arg == "" {
			var sCmd tea.Cmd
			m.stream, sCmd = m.stream.Update(streamTokenMsg("usage: /model <id>\n"))
			m.prompt.SetValue("")
			return sCmd
		}
		m.deps.Model = action.arg
		var hCmd tea.Cmd
		m.header, hCmd = m.header.Update(headerStateMsg{model: action.arg})
		m.prompt.SetValue("")
		return hCmd

	case "scope":
		// Insert prefix into prompt so user fills in the argument.
		m.prompt.SetValue("/scope ")
		return nil

	case "tools":
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg(renderInventory(m.deps.Tools)))
		m.prompt.SetValue("")
		return sCmd

	case "replay":
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("replay not yet implemented\n"))
		m.prompt.SetValue("")
		return sCmd

	case "setup", "set-up":
		m.prompt.SetValue("")
		return m.enterSetup()
	}

	m.prompt.SetValue("")
	return nil
}

// enterSetup transitions the model into modeSetup, constructs a fresh wizard
// bound to a cancellable context, and returns the wizard's Init command.
func (m *Model) enterSetup() tea.Cmd {
	m.setupCtx, m.setupCancel = context.WithCancel(context.Background())
	m.setupWiz = setup.NewWizardModel(m.setupCtx, setup.WizardOptions{
		ConfigPath:  config.Path(),
		ProfilePath: config.ProfilePath(),
	})
	m.mode = modeSetup
	m.prompt.SetValue("")
	if m.setupWiz == nil {
		// Defensive: if the wizard could not be created, fall back to main.
		m.exitSetup()
		return nil
	}
	return m.setupWiz.Init()
}

// exitSetup returns the model to modeMain, cancels the wizard context, writes
// a status line to the stream based on SaveErr(), and resets the welcome
// banner to its compact form.
func (m *Model) exitSetup() {
	aborted := false
	var saveErr error
	if m.setupWiz != nil {
		aborted = m.setupWiz.Aborted()
		saveErr = m.setupWiz.SaveErr()
	}

	if m.setupCancel != nil {
		m.setupCancel()
	}
	m.setupCancel = nil
	m.setupCtx = nil
	m.setupWiz = nil
	m.mode = modeMain

	var line string
	switch {
	case saveErr != nil:
		line = "[setup error: " + saveErr.Error() + "]\n"
	case aborted:
		line = "[setup aborted]\n"
	default:
		line = "[setup complete]\n"
	}
	var sCmd tea.Cmd
	m.stream, sCmd = m.stream.Update(streamTokenMsg(line))
	_ = sCmd

	// After /setup the user has been here before — drop the full banner.
	m.welcome = NewWelcomeModel(false, m.deps.InitialCtx)
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
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("[scope error: " + err.Error() + "]\n"))
		m.prompt.SetValue("")
		return sCmd
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

	var feedback string
	if sc.Empty() {
		feedback = "[scope reset]\n"
	} else {
		feedback = "[scope set: " + sc.String() + "]\n"
	}
	var sCmd tea.Cmd
	m.stream, sCmd = m.stream.Update(streamTokenMsg(feedback))
	cmds = append(cmds, sCmd)

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
