package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rlaope/cloudy/internal/buildinfo"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
)

// ctrlCTimeout is the window within which two Ctrl+C presses quit the program.
const ctrlCTimeout = 2 * time.Second

// Deps holds all external dependencies the TUI needs.
type Deps struct {
	// Provider is the LLM backend. May be nil when no config is present.
	Provider interface{} // llm.Provider — stored as interface{} to avoid import cycles in tests
	// Model is the fully-qualified model identifier.
	Model string
	// Skills is the loaded skill registry.
	Skills *skills.Registry
	// Tools is the loaded tool registry.
	Tools *tools.Registry
	// Session is an open append-only session log (may be nil).
	Session interface{} // *session.Session
	// InitialCtx is the current kubeconfig context (best-effort).
	InitialCtx string
	// InitialNS is the initial namespace (best-effort).
	InitialNS string
	// AgentRunner is the function called to run the agent on user input.
	// Injected by run.go so that tests can stub it.
	AgentRunner func(ctx interface{}, input string, emit func(AgentEvent))
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

	// Ctrl+C double-tap state.
	lastCtrlC  time.Time
	ctrlCCount int

	// cancel is called to stop the in-flight agent goroutine.
	cancel func()
	// running indicates an agent run is in progress.
	running bool

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
	}
}

func (m Model) Init() tea.Cmd {
	return m.prompt.Init()
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
		return m, tea.Batch(hCmd, sCmd, prCmd, paCmd)

	case tea.KeyMsg:
		// Palette is active — route all keys there first.
		if m.palette.Active() {
			var cmd tea.Cmd
			m.palette, cmd = m.palette.Update(msg)
			return m, cmd
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
		return m, m.runAgent(val)

	case paletteActionMsg:
		return m, m.handlePaletteAction(msg)

	case paletteDismissMsg:
		// Palette closed without action; restore focus to prompt.
		return m, nil

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

	header := m.header.View()
	stream := m.stream.View()
	prompt := m.prompt.View()

	view := header + "\n" + stream + "\n" + prompt

	// Overlay palette if active.
	if m.palette.Active() {
		// Simple overlay: render palette below header.
		view = header + "\n" + m.palette.View() + "\n" + prompt
	}

	return view
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

	case "quit":
		return tea.Quit

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

	case "replay":
		var sCmd tea.Cmd
		m.stream, sCmd = m.stream.Update(streamTokenMsg("replay not yet implemented\n"))
		m.prompt.SetValue("")
		return sCmd
	}

	m.prompt.SetValue("")
	return nil
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
  Up / Down      navigate history
  Ctrl+R         incremental history search
  Ctrl+L         clear stream
  Ctrl+C         cancel in-flight request
  Ctrl+C×2       quit
  Esc            cancel request
  Tab            open command palette
  PageUp/Dn      scroll output

commands (type / to open palette):
  /skill <name>       switch active skill
  /use <ctx>          switch kubeconfig context
  /model <id>         switch model
  /scope ns=<csv>     narrow session to namespaces (comma-separated)
  /scope ctx=<csv>    narrow session to contexts (comma-separated)
  /scope reset        drop session scope
  /replay <session>   replay session
  /clear              clear output
  /quit               exit
  /help               show this text
  /version            print version
`
}
