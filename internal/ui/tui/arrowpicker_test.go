package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestArrowPicker_MoveWraps confirms the cursor wraps around at both
// ends — same affordance Claude's HITL picker provides so the
// operator never has to think about list bounds.
func TestArrowPicker_MoveWraps(t *testing.T) {
	p := newArrowPicker("pick:", []arrowPickerItem{
		{label: "a", key: "a"},
		{label: "b", key: "b"},
		{label: "c", key: "c"},
	})
	if p.Selected().key != "a" {
		t.Errorf("initial selection should be first item, got %q", p.Selected().key)
	}
	p.MoveUp() // wrap to c
	if p.Selected().key != "c" {
		t.Errorf("MoveUp from index 0 should wrap to last, got %q", p.Selected().key)
	}
	p.MoveDown() // wrap to a
	if p.Selected().key != "a" {
		t.Errorf("MoveDown from last should wrap to first, got %q", p.Selected().key)
	}
}

// TestArrowPicker_Empty handles the edge case where Selected() is
// called on an empty picker. Should return the zero value, not panic.
func TestArrowPicker_Empty(t *testing.T) {
	p := newArrowPicker("nothing:", nil)
	if got := p.Selected(); got.key != "" || got.label != "" {
		t.Errorf("empty picker should return zero Selected(), got %+v", got)
	}
	p.MoveUp()
	p.MoveDown()
	// no panic = pass
}

// TestArrowPicker_View_HasCursorAndHints renders the picker and checks
// the visible affordances are present. Not a pixel diff — just confirms
// the cursor row marker and the key-help footer reach the screen.
func TestArrowPicker_View_HasCursorAndHints(t *testing.T) {
	p := newArrowPicker("Pick one:", []arrowPickerItem{
		{label: "alpha", hint: "first letter", key: "a"},
		{label: "beta", hint: "second letter", key: "b"},
	})
	view := p.View()
	if !strings.Contains(view, "Pick one:") {
		t.Errorf("view should include the title, got: %q", view)
	}
	if !strings.Contains(view, "▸") {
		t.Errorf("view should include the cursor glyph ▸, got: %q", view)
	}
	if !strings.Contains(view, "↑↓ to move") {
		t.Errorf("view should include the key-help footer, got: %q", view)
	}
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") {
		t.Errorf("view should include every label, got: %q", view)
	}
}

// TestArrowPicker_View_WindowsLongList confirms a list longer than
// pickerMaxVisible only renders a window of rows around the cursor plus
// "↑/↓ N more" markers, so the bare /skill menu stays compact instead of
// dumping every registered skill.
func TestArrowPicker_View_WindowsLongList(t *testing.T) {
	items := make([]arrowPickerItem, 15)
	for i := range items {
		k := string(rune('a' + i))
		items[i] = arrowPickerItem{label: "label-" + k, key: k}
	}
	p := newArrowPicker("Pick:", items)

	// Cursor at the top: first pickerMaxVisible rows, a "more" marker below,
	// none above, and a far row must be off-screen.
	view := p.View()
	if !strings.Contains(view, "label-a") {
		t.Errorf("top of window should be visible, got: %q", view)
	}
	if strings.Contains(view, "label-o") { // index 14, far below
		t.Errorf("a row past the window must not render, got: %q", view)
	}
	if !strings.Contains(view, "↓ 9 more") {
		t.Errorf("a longer list must show a ↓ more marker, got: %q", view)
	}
	if strings.Contains(view, "↑ 9 more") {
		t.Errorf("at the top there is nothing above, so no ↑ marker, got: %q", view)
	}

	// Move to the last row: the window scrolls so the tail is visible with an
	// ↑ marker and no ↓ marker.
	p.MoveUp() // wraps to index 14 (last)
	tail := p.View()
	if !strings.Contains(tail, "label-o") {
		t.Errorf("cursor on the last row should keep it visible, got: %q", tail)
	}
	if !strings.Contains(tail, "↑ 9 more") {
		t.Errorf("scrolled-down window should show a ↑ more marker, got: %q", tail)
	}
	if strings.Contains(tail, "↓ 9 more") {
		t.Errorf("at the bottom there is nothing below, so no ↓ marker, got: %q", tail)
	}
}

// TestPickerWindow covers the pure windowing math: short lists render whole,
// long lists keep the cursor centred and clamp at both ends.
func TestPickerWindow(t *testing.T) {
	cases := []struct {
		name               string
		cursor, n, max     int
		wantStart, wantEnd int
	}{
		{"shorter than max renders whole", 0, 4, 6, 0, 4},
		{"exactly max renders whole", 5, 6, 6, 0, 6},
		{"cursor at top clamps start to 0", 0, 15, 6, 0, 6},
		{"cursor centred mid-list", 8, 15, 6, 5, 11},
		{"cursor at end clamps to tail", 14, 15, 6, 9, 15},
	}
	for _, tc := range cases {
		gotStart, gotEnd := pickerWindow(tc.cursor, tc.n, tc.max)
		if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
			t.Errorf("%s: pickerWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
				tc.name, tc.cursor, tc.n, tc.max, gotStart, gotEnd, tc.wantStart, tc.wantEnd)
		}
	}
}

// TestArrowPicker_ResolveCmd_FiresMsg wraps a selection in
// arrowPickerResolveCmd and confirms the returned tea.Cmd produces an
// arrowPickerResolveMsg with the right key + cancelled flag.
func TestArrowPicker_ResolveCmd_FiresMsg(t *testing.T) {
	cmd := arrowPickerResolveCmd("openai", false)
	if cmd == nil {
		t.Fatal("arrowPickerResolveCmd should never return nil")
	}
	msg := cmd()
	resolve, ok := msg.(arrowPickerResolveMsg)
	if !ok {
		t.Fatalf("cmd should fire arrowPickerResolveMsg, got %T", msg)
	}
	if resolve.key != "openai" || resolve.cancelled {
		t.Errorf("unexpected resolve payload: %+v", resolve)
	}

	cancelCmd := arrowPickerResolveCmd("", true)
	cancelMsg, _ := cancelCmd().(arrowPickerResolveMsg)
	if !cancelMsg.cancelled {
		t.Errorf("cancel cmd should set cancelled=true, got %+v", cancelMsg)
	}
}

// TestLogin_ArrowPicker_EndToEnd drives the parent Model through a
// complete picker resolve flow: /login activates the picker, an
// arrowPickerResolveMsg with key="google" advances loginChat to its
// key-prompt step.
func TestLogin_ArrowPicker_EndToEnd(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "login"})
	if cmd != nil {
		cmd()
	}
	if m.arrowPicker == nil {
		t.Fatal("/login should activate an arrowPicker")
	}
	if m.loginChat == nil {
		t.Fatal("/login should start a loginChat")
	}

	// Simulate the operator hitting Enter on the "google" row.
	resolveCmd := arrowPickerResolveCmd("google", false)
	next, outCmd := m.Update(resolveCmd().(tea.Msg))
	m = next.(Model)

	if m.arrowPicker != nil {
		t.Error("picker should be cleared after resolve")
	}
	if m.loginChat == nil {
		t.Fatal("loginChat should stay active for the key prompt")
	}
	if m.loginChat.provider != "google" {
		t.Errorf("loginChat.provider should be google, got %q", m.loginChat.provider)
	}
	if !strings.Contains(printedText(outCmd), "GOOGLE_API_KEY") {
		t.Errorf("scrollback should contain key-prompt for GOOGLE_API_KEY, got: %q",
			printedText(outCmd))
	}
}

// TestLogin_ArrowPicker_Cancel resolves the picker with cancelled=true
// and confirms loginChat ends instead of advancing to step 1.
func TestLogin_ArrowPicker_Cancel(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "login"})
	if cmd != nil {
		cmd()
	}
	resolveCmd := arrowPickerResolveCmd("", true)
	next, outCmd := m.Update(resolveCmd().(tea.Msg))
	m = next.(Model)

	if m.loginChat != nil {
		t.Error("loginChat should be cleared after cancel resolve")
	}
	if !strings.Contains(printedText(outCmd), "cancelled") {
		t.Errorf("scrollback should contain cancellation note, got: %q",
			printedText(outCmd))
	}
}
