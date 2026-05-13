package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// paletteActionMsg is sent when the user selects a palette item.
type paletteActionMsg struct {
	cmd  string // e.g. "skill", "model", "clear", "quit"
	arg  string // e.g. skill name, model id
	full string // the full raw typed command
}

// paletteDismissMsg is sent when the palette is dismissed without selection.
type paletteDismissMsg struct{}

// paletteItem implements list.Item.
type paletteItem struct {
	title string
	usage string
}

func (i paletteItem) Title() string       { return i.title }
func (i paletteItem) Description() string { return i.usage }
func (i paletteItem) FilterValue() string { return i.title }

var builtinItems = []list.Item{
	paletteItem{title: "skill", usage: "/skill <name>  — switch active skill"},
	paletteItem{title: "use", usage: "/use <ctx>     — switch kubeconfig context"},
	paletteItem{title: "model", usage: "/model <id>   — switch active model"},
	paletteItem{title: "replay", usage: "/replay <session> — replay a session file"},
	paletteItem{title: "clear", usage: "/clear        — clear stream output"},
	paletteItem{title: "quit", usage: "/quit         — exit cloudy"},
	paletteItem{title: "help", usage: "/help         — show help text"},
	paletteItem{title: "version", usage: "/version      — print build version"},
}

// PaletteModel is a command palette triggered by typing `/`.
type PaletteModel struct {
	list    list.Model
	active  bool
	rawText string // full typed text including '/'

	frameStyle lipgloss.Style
}

func newPaletteModel() PaletteModel {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	l := list.New(builtinItems, delegate, 40, 12)
	l.Title = "Commands"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()

	frameStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1)

	return PaletteModel{
		list:       l,
		frameStyle: frameStyle,
	}
}

// Open activates the palette with the current raw prompt text.
func (p *PaletteModel) Open(rawText string) {
	p.active = true
	p.rawText = rawText

	// Pre-filter the list based on what the user typed after '/'.
	query := strings.TrimPrefix(rawText, "/")
	p.list.ResetFilter()
	if query != "" {
		p.list.SetFilterText(query)
	}
}

// Close deactivates the palette.
func (p *PaletteModel) Close() {
	p.active = false
	p.rawText = ""
	p.list.ResetFilter()
}

// Active reports whether the palette is open.
func (p PaletteModel) Active() bool { return p.active }

func (p PaletteModel) Init() tea.Cmd { return nil }

func (p PaletteModel) Update(msg tea.Msg) (PaletteModel, tea.Cmd) {
	if !p.active {
		return p, nil
	}

	switch m := msg.(type) {
	case tea.KeyMsg:
		switch m.String() {
		case "esc":
			p.Close()
			return p, func() tea.Msg { return paletteDismissMsg{} }

		case "enter":
			item, ok := p.list.SelectedItem().(paletteItem)
			if !ok {
				p.Close()
				return p, func() tea.Msg { return paletteDismissMsg{} }
			}
			action := p.buildAction(item.title)
			p.Close()
			return p, func() tea.Msg { return action }

		case "tab":
			// Tab completes the selected item's title into the prompt.
			item, ok := p.list.SelectedItem().(paletteItem)
			if ok {
				p.Close()
				return p, func() tea.Msg {
					return paletteActionMsg{cmd: "tab-complete", arg: item.title}
				}
			}
		}

	case tea.WindowSizeMsg:
		h := m.Height / 2
		if h < 8 {
			h = 8
		}
		w := m.Width / 2
		if w < 40 {
			w = 40
		}
		p.list.SetSize(w, h)
	}

	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, cmd
}

func (p PaletteModel) View() string {
	if !p.active {
		return ""
	}
	return p.frameStyle.Render(p.list.View())
}

// buildAction parses the raw typed text plus the selected title into a paletteActionMsg.
func (p PaletteModel) buildAction(selectedTitle string) paletteActionMsg {
	// Extract argument from raw text if user already typed it.
	// e.g. rawText = "/skill k8s-incident"
	raw := strings.TrimPrefix(p.rawText, "/")
	parts := strings.Fields(raw)

	cmd := selectedTitle
	arg := ""
	if len(parts) >= 2 {
		// User typed both command and arg.
		arg = strings.Join(parts[1:], " ")
	}

	return paletteActionMsg{
		cmd:  cmd,
		arg:  arg,
		full: p.rawText,
	}
}
