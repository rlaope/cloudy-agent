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
//
// When multiSelect is true the picker behaves like Claude's checkbox
// list: Space toggles the row under the cursor, Enter confirms ALL
// currently-selected rows (sending an arrowPickerMultiResolveMsg),
// Esc cancels. The View renders `[x]` / `[ ]` glyphs in place of the
// usual cursor arrow so the operator sees what's about to be committed
// before pressing Enter.
type arrowPicker struct {
	title       string
	items       []arrowPickerItem
	cursor      int
	multiSelect bool
	selected    map[int]bool
}

func newArrowPicker(title string, items []arrowPickerItem) *arrowPicker {
	return &arrowPicker{title: title, items: items}
}

// newMultiArrowPicker builds a checkbox-style picker. preselected lists
// the keys that should start in the selected state — typically "all"
// when /setup wants to default-include every detected backend so the
// operator only has to un-tick the ones they don't want.
func newMultiArrowPicker(title string, items []arrowPickerItem, preselected []string) *arrowPicker {
	p := &arrowPicker{
		title:       title,
		items:       items,
		multiSelect: true,
		selected:    make(map[int]bool, len(items)),
	}
	if len(preselected) > 0 {
		want := make(map[string]bool, len(preselected))
		for _, k := range preselected {
			want[k] = true
		}
		for i, it := range items {
			if want[it.key] {
				p.selected[i] = true
			}
		}
	}
	return p
}

// Toggle flips the selected state of the row under the cursor. No-op
// for single-select pickers.
func (p *arrowPicker) Toggle() {
	if !p.multiSelect || len(p.items) == 0 {
		return
	}
	if p.selected == nil {
		p.selected = map[int]bool{}
	}
	p.selected[p.cursor] = !p.selected[p.cursor]
}

// SelectedKeys returns the keys of every currently-ticked item in the
// order they appear in items. Empty slice when nothing is ticked.
// Single-select pickers always return the empty slice; use Selected()
// for those.
func (p *arrowPicker) SelectedKeys() []string {
	if !p.multiSelect {
		return nil
	}
	out := make([]string, 0, len(p.selected))
	for i, it := range p.items {
		if p.selected[i] {
			out = append(out, it.key)
		}
	}
	return out
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

// pickerMaxVisible bounds how many rows the picker renders at once. A longer
// list (e.g. the full skills registry) scrolls a window of this size around
// the cursor with "↑ N more" / "↓ N more" markers instead of dumping every
// row, which is what made the bare /skill menu feel cluttered.
const pickerMaxVisible = 6

// pickerWindow returns the [start,end) range of items to render so the cursor
// stays visible and roughly centred. Lists at or under pickerMaxVisible render
// whole (start 0, end n).
func pickerWindow(cursor, n, max int) (int, int) {
	if n <= max {
		return 0, n
	}
	start := cursor - max/2
	if start < 0 {
		start = 0
	}
	if start > n-max {
		start = n - max
	}
	return start, start + max
}

func (p *arrowPicker) View() string {
	if p == nil || len(p.items) == 0 {
		return ""
	}
	var b strings.Builder
	if p.title != "" {
		b.WriteString(pickerTitleStyle.Render(p.title))
		b.WriteString("\n")
	}
	start, end := pickerWindow(p.cursor, len(p.items), pickerMaxVisible)
	if start > 0 {
		fmt.Fprintf(&b, "%s\n", pickerHintStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		it := p.items[i]
		cursor := "  "
		label := pickerLabelDimStyle.Render(it.label)
		if i == p.cursor {
			cursor = pickerCursorStyle.Render("▸ ")
			label = pickerLabelStyle.Render(it.label)
		}
		row := cursor
		if p.multiSelect {
			if p.selected[i] {
				row += pickerCursorStyle.Render("[x] ")
			} else {
				row += pickerHintStyle.Render("[ ] ")
			}
		}
		row += label
		if it.hint != "" {
			row += "  " + pickerHintStyle.Render(it.hint)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	if end < len(p.items) {
		fmt.Fprintf(&b, "%s\n", pickerHintStyle.Render(fmt.Sprintf("  ↓ %d more", len(p.items)-end)))
	}
	hint := "  ↑↓ to move · Enter to select · Esc to cancel"
	if p.multiSelect {
		hint = "  ↑↓ to move · Space to toggle · Enter to confirm · Esc to cancel"
	}
	b.WriteString(pickerHintStyle.Render(hint))
	return b.String()
}

// arrowPickerResolveMsg is the bubbletea message dispatched by a
// single-select picker when the operator hits Enter (key is the
// selected item's key field) or Esc (cancelled is true; key is empty).
type arrowPickerResolveMsg struct {
	key       string
	cancelled bool
}

// arrowPickerMultiResolveMsg is the multi-select counterpart. keys
// carries every currently-ticked row in display order, possibly empty
// when the operator confirmed with nothing ticked. cancelled fires on
// Esc and leaves keys nil so the receiver can distinguish "operator
// said: I want none" from "operator backed out".
type arrowPickerMultiResolveMsg struct {
	keys      []string
	cancelled bool
}

// resolveCmd returns a tea.Cmd that fires an arrowPickerResolveMsg.
// Wrapping the resolution in a tea.Cmd keeps the parent's Update
// strictly message-driven, the same shape as every other event.
func arrowPickerResolveCmd(key string, cancelled bool) tea.Cmd {
	msg := arrowPickerResolveMsg{key: key, cancelled: cancelled}
	return func() tea.Msg { return msg }
}

// arrowPickerMultiResolveCmd fires the multi-select counterpart.
func arrowPickerMultiResolveCmd(keys []string, cancelled bool) tea.Cmd {
	msg := arrowPickerMultiResolveMsg{keys: keys, cancelled: cancelled}
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
