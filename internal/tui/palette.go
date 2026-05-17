package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// paletteActionMsg is sent when the user selects a palette item.
type paletteActionMsg struct {
	cmd  string // e.g. "skill", "model", "clear", "quit"
	arg  string // e.g. skill name, model id
	full string // the full raw typed command
}

// paletteDismissMsg is sent when the palette is dismissed without selection
// (Esc) so the parent Model can clear the prompt's leading '/' if it wants.
type paletteDismissMsg struct{}

// paletteEscalateMsg is sent when the palette wants to forward a key it does
// not handle (currently only Ctrl+C) back to the parent Model. The parent
// closes the palette and re-applies the key to the main key handler.
type paletteEscalateMsg struct {
	key tea.KeyMsg
}

// paletteItem is one row in the suggestions list.
type paletteItem struct {
	title string
	usage string
}

// builtinItems is the canonical list of slash commands, in display order.
// The same slice powers the suggestion view and the registration tests.
var builtinItems = []paletteItem{
	{title: "setup", usage: "/setup        — run the discovery wizard"},
	{title: "set-up", usage: "/set-up       — alias of /setup; re-analyse the cluster"},
	{title: "skill", usage: "/skill <name> — switch active skill"},
	{title: "use", usage: "/use <ctx>    — switch kubeconfig context"},
	{title: "model", usage: "/model <id>   — switch active model"},
	{title: "scope", usage: "/scope ns=… | ctx=… | reset"},
	{title: "tools", usage: "/tools        — list registered tool groups"},
	{title: "replay", usage: "/replay <id>  — replay a session file"},
	{title: "clear", usage: "/clear        — clear stream output"},
	{title: "update", usage: "/update       — show install commands for the latest cloudy"},
	{title: "help", usage: "/help         — show help text"},
	{title: "version", usage: "/version      — print build version"},
	{title: "exit", usage: "/exit         — quit cloudy (alias of /quit)"},
	{title: "quit", usage: "/quit         — exit cloudy"},
}

// paletteMaxRows is the maximum number of suggestion rows shown at once.
// Bounded so the palette never eats the whole stream area on small terminals.
const paletteMaxRows = 6

// PaletteModel is a compact suggestion dropdown rendered directly below the
// prompt (Claude-style). It does not own focus or a viewport — typing still
// goes to the prompt textarea; the palette only filters and previews the
// matching commands and announces a selection via paletteActionMsg.
type PaletteModel struct {
	active  bool
	rawText string // full typed text including '/'

	matches []paletteItem
	cursor  int

	width int // updated from WindowSizeMsg

	frameStyle  lipgloss.Style
	cursorStyle lipgloss.Style
	titleStyle  lipgloss.Style
	usageStyle  lipgloss.Style
}

func newPaletteModel() PaletteModel {
	return PaletteModel{
		width: 80,
		frameStyle: lipgloss.NewStyle().
			BorderForeground(lipgloss.Color("240")),
		cursorStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).Bold(true),
		titleStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).Bold(true),
		usageStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")),
	}
}

// Open activates the palette with the current raw prompt text and rebuilds
// the filtered match list. Idempotent: calling Open again on an already-open
// palette refreshes the filter (useful as the user keeps typing past '/').
func (p *PaletteModel) Open(rawText string) {
	p.active = true
	p.rawText = rawText
	p.rebuildMatches()
}

// Refilter refreshes the match list against rawText without touching active.
// Called from the parent whenever the prompt text changes while the palette
// is open.
func (p *PaletteModel) Refilter(rawText string) {
	p.rawText = rawText
	p.rebuildMatches()
}

// Close deactivates the palette.
func (p *PaletteModel) Close() {
	p.active = false
	p.rawText = ""
	p.matches = nil
	p.cursor = 0
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
		case "ctrl+c":
			// Forward Ctrl+C to the parent so the user can always cancel /
			// quit, no matter what's on screen.
			key := m
			p.Close()
			return p, func() tea.Msg { return paletteEscalateMsg{key: key} }

		case "esc":
			p.Close()
			return p, func() tea.Msg { return paletteDismissMsg{} }

		case "enter":
			if len(p.matches) == 0 {
				p.Close()
				return p, func() tea.Msg { return paletteDismissMsg{} }
			}
			selected := p.matches[p.cursor]
			action := p.buildAction(selected.title)
			p.Close()
			return p, func() tea.Msg { return action }

		case "tab":
			if len(p.matches) == 0 {
				return p, nil
			}
			selected := p.matches[p.cursor]
			p.Close()
			return p, func() tea.Msg {
				return paletteActionMsg{cmd: "tab-complete", arg: selected.title}
			}

		case "up":
			if len(p.matches) > 0 {
				p.cursor = (p.cursor - 1 + len(p.matches)) % len(p.matches)
			}
			return p, nil

		case "down":
			if len(p.matches) > 0 {
				p.cursor = (p.cursor + 1) % len(p.matches)
			}
			return p, nil
		}

	case tea.WindowSizeMsg:
		p.width = m.Width
	}

	return p, nil
}

// View renders the suggestion dropdown. Empty string when inactive so the
// parent can compose it conditionally.
func (p PaletteModel) View() string {
	if !p.active || len(p.matches) == 0 {
		return ""
	}
	var b strings.Builder
	max := len(p.matches)
	if max > paletteMaxRows {
		max = paletteMaxRows
	}
	// Scroll window so cursor is visible when we have more matches than rows.
	start := 0
	if p.cursor >= paletteMaxRows {
		start = p.cursor - paletteMaxRows + 1
	}
	for i := start; i < start+max && i < len(p.matches); i++ {
		row := p.renderRow(p.matches[i], i == p.cursor)
		b.WriteString(row)
		if i < start+max-1 && i < len(p.matches)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (p PaletteModel) renderRow(item paletteItem, selected bool) string {
	cursor := "  "
	title := p.titleStyle.Render(item.title)
	if selected {
		cursor = p.cursorStyle.Render("▸ ")
		title = p.cursorStyle.Render(item.title)
	}
	usage := p.usageStyle.Render(item.usage)
	return cursor + title + "  " + usage
}

// rebuildMatches refilters builtinItems against the substring after '/'.
// An empty filter returns the full list in declared order.
func (p *PaletteModel) rebuildMatches() {
	query := strings.ToLower(strings.TrimPrefix(p.rawText, "/"))
	// Strip any space-separated argument; only the command verb is the filter
	// target (e.g. typing "/skill k" filters by "skill", not "skill k").
	if i := strings.IndexByte(query, ' '); i >= 0 {
		query = query[:i]
	}

	p.matches = p.matches[:0]
	if query == "" {
		for _, it := range builtinItems {
			p.matches = append(p.matches, it)
		}
	} else {
		// First pass: prefix matches.
		for _, it := range builtinItems {
			if strings.HasPrefix(it.title, query) {
				p.matches = append(p.matches, it)
			}
		}
		// Second pass: substring matches that were not already prefix matches.
		for _, it := range builtinItems {
			if strings.HasPrefix(it.title, query) {
				continue
			}
			if strings.Contains(it.title, query) {
				p.matches = append(p.matches, it)
			}
		}
	}
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

// buildAction parses the raw typed text plus the selected title into a
// paletteActionMsg. Anything after the first space is treated as the arg
// (e.g. "/skill k8s-incident" → cmd=skill arg=k8s-incident).
func (p PaletteModel) buildAction(selectedTitle string) paletteActionMsg {
	raw := strings.TrimPrefix(p.rawText, "/")
	parts := strings.Fields(raw)

	arg := ""
	if len(parts) >= 2 {
		arg = strings.Join(parts[1:], " ")
	}

	return paletteActionMsg{
		cmd:  selectedTitle,
		arg:  arg,
		full: p.rawText,
	}
}
