package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// arrowPickerItem is one row in the picker. label is the bold left
// column, hint is the dim trailing description, key is the value sent
// to the resolver on Enter.
type arrowPickerItem struct {
	label string
	hint  string
	key   string
}

// arrowPicker renders a Claude-style HITL menu — bold cursor row,
// arrow keys to navigate, Enter to confirm, Esc to abort. It does not
// own its own goroutine or focus state; the parent Model gates input
// to the picker by checking arrowPicker != nil in the key handler.
type arrowPicker struct {
	title  string
	items  []arrowPickerItem
	cursor int
}

func newArrowPicker(title string, items []arrowPickerItem) *arrowPicker {
	return &arrowPicker{title: title, items: items}
}

// MoveUp wraps the cursor.
func (p *arrowPicker) MoveUp() {
	if len(p.items) == 0 {
		return
	}
	p.cursor = (p.cursor - 1 + len(p.items)) % len(p.items)
}

// MoveDown wraps the cursor.
func (p *arrowPicker) MoveDown() {
	if len(p.items) == 0 {
		return
	}
	p.cursor = (p.cursor + 1) % len(p.items)
}

// Selected returns the item the cursor is currently on, or the zero
// value when the picker is empty.
func (p *arrowPicker) Selected() arrowPickerItem {
	if len(p.items) == 0 {
		return arrowPickerItem{}
	}
	return p.items[p.cursor]
}

// View renders the picker as a small framed block.
//
//	title
//	▸ label   hint
//	  label   hint
//	  label   hint
//	  ↑↓ to move · Enter to select · Esc to cancel
var (
	pickerTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true)
	pickerCursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	pickerLabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	pickerLabelDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	pickerHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func (p *arrowPicker) View() string {
	if p == nil || len(p.items) == 0 {
		return ""
	}
	var b strings.Builder
	if p.title != "" {
		b.WriteString(pickerTitleStyle.Render(p.title))
		b.WriteString("\n")
	}
	for i, it := range p.items {
		cursor := "  "
		label := pickerLabelDimStyle.Render(it.label)
		if i == p.cursor {
			cursor = pickerCursorStyle.Render("▸ ")
			label = pickerLabelStyle.Render(it.label)
		}
		row := cursor + label
		if it.hint != "" {
			row += "  " + pickerHintStyle.Render(it.hint)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	b.WriteString(pickerHintStyle.Render("  ↑↓ to move · Enter to select · Esc to cancel"))
	return b.String()
}

// arrowPickerResolveMsg is the bubbletea message dispatched by the
// picker when the operator hits Enter (key is the selected item's key
// field) or Esc (cancelled is true; key is empty).
type arrowPickerResolveMsg struct {
	key       string
	cancelled bool
}

// resolveCmd returns a tea.Cmd that fires an arrowPickerResolveMsg.
// Wrapping the resolution in a tea.Cmd keeps the parent's Update
// strictly message-driven, the same shape as every other event.
func arrowPickerResolveCmd(key string, cancelled bool) tea.Cmd {
	msg := arrowPickerResolveMsg{key: key, cancelled: cancelled}
	return func() tea.Msg { return msg }
}

// describe is a debug helper used by tests; it returns a compact
// "title[cursor:label]" string for cheap equality assertions without
// having to diff full View() output.
func (p *arrowPicker) describe() string {
	if p == nil {
		return "(nil)"
	}
	if len(p.items) == 0 {
		return fmt.Sprintf("%q[]", p.title)
	}
	return fmt.Sprintf("%q[%d:%s]", p.title, p.cursor, p.items[p.cursor].key)
}
