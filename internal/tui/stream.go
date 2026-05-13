package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// streamTokenMsg carries a text fragment to append to the stream viewport.
type streamTokenMsg string

// streamToolBeginMsg signals the start of a tool call block.
type streamToolBeginMsg struct {
	name string
	args string
}

// streamToolEndMsg signals the end of a tool call block.
type streamToolEndMsg struct {
	observation string
	err         error
}

// streamClearMsg clears the stream viewport.
type streamClearMsg struct{}

// toolBlock tracks fold state for a tool call.
type toolBlock struct {
	name        string
	args        string
	observation string
	err         error
	folded      bool
}

// StreamModel backs the scrollable output area using a bubbles/viewport.
type StreamModel struct {
	vp      viewport.Model
	content strings.Builder
	ready   bool

	// pending tool block being assembled
	pendingTool *toolBlock

	toolStyle lipgloss.Style
	obsStyle  lipgloss.Style
	errStyle  lipgloss.Style
	noColor   bool
}

func newStreamModel(noColor bool) StreamModel {
	s := StreamModel{noColor: noColor}
	if !noColor {
		s.toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		s.obsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		s.errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	}
	return s
}

func (s StreamModel) Init() tea.Cmd { return nil }

func (s StreamModel) Update(msg tea.Msg) (StreamModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		headerHeight := 1
		promptHeight := 3
		vpHeight := m.Height - headerHeight - promptHeight
		if vpHeight < 1 {
			vpHeight = 1
		}
		if !s.ready {
			s.vp = viewport.New(m.Width, vpHeight)
			s.vp.SetContent(s.content.String())
			s.ready = true
		} else {
			s.vp.Width = m.Width
			s.vp.Height = vpHeight
		}

	case streamTokenMsg:
		s.content.WriteString(string(m))
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}

	case streamToolBeginMsg:
		s.pendingTool = &toolBlock{name: m.name, args: m.args}
		header := "▶ tool: " + m.name + "(" + m.args + ")"
		if !s.noColor {
			header = s.toolStyle.Render(header)
		}
		s.content.WriteString("\n" + header + "\n")
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}

	case streamToolEndMsg:
		if s.pendingTool != nil {
			s.pendingTool.observation = m.observation
			s.pendingTool.err = m.err
			s.pendingTool = nil
		}
		if m.err != nil {
			errLine := "  error: " + m.err.Error()
			if !s.noColor {
				errLine = s.errStyle.Render(errLine)
			}
			s.content.WriteString(errLine + "\n")
		}
		if m.observation != "" {
			obs := indentObs(m.observation, "  ")
			if !s.noColor {
				obs = s.obsStyle.Render(obs)
			}
			s.content.WriteString(obs + "\n")
		}
		if s.ready {
			s.vp.SetContent(s.content.String())
			s.vp.GotoBottom()
		}

	case streamClearMsg:
		s.content.Reset()
		s.pendingTool = nil
		if s.ready {
			s.vp.SetContent("")
		}
	}

	if s.ready {
		s.vp, cmd = s.vp.Update(msg)
		cmds = append(cmds, cmd)
	}

	return s, tea.Batch(cmds...)
}

func (s StreamModel) View() string {
	if !s.ready {
		return ""
	}
	return s.vp.View()
}

// indentObs prepends prefix to every non-empty line.
func indentObs(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
