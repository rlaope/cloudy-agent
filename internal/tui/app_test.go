package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// makeDeps returns a minimal Deps suitable for unit tests (no real provider).
func makeDeps() Deps {
	return Deps{
		Model:      "test-model",
		InitialCtx: "test-ctx",
		InitialNS:  "test-ns",
	}
}

// windowMsg returns a standard WindowSizeMsg to initialise sub-models.
func windowMsg() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: 120, Height: 40}
}

// sendKeys drives the model through a sequence of key messages and returns the
// final model and the last non-nil Msg produced by any Cmd.
func sendKey(m Model, key string) (Model, tea.Msg) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if len(key) == 1 {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	nm := next.(Model)
	var produced tea.Msg
	if cmd != nil {
		produced = cmd()
	}
	return nm, produced
}

func sendSpecialKey(m Model, t tea.KeyType) (Model, tea.Msg) {
	msg := tea.KeyMsg{Type: t}
	next, cmd := m.Update(msg)
	nm := next.(Model)
	var produced tea.Msg
	if cmd != nil {
		produced = cmd()
	}
	return nm, produced
}

func TestModel_Init(t *testing.T) {
	m := NewModel(makeDeps())
	cmd := m.Init()
	// Init returns a textarea.Blink command (non-nil).
	if cmd == nil {
		t.Error("Init() returned nil cmd, want non-nil (blink)")
	}
}

func TestModel_WindowSize_SetsReady(t *testing.T) {
	m := NewModel(makeDeps())
	if m.ready {
		t.Fatal("model should not be ready before WindowSizeMsg")
	}
	next, _ := m.Update(windowMsg())
	nm := next.(Model)
	if !nm.ready {
		t.Error("model should be ready after WindowSizeMsg")
	}
}

func TestModel_TypingText_DoesNotOpenPalette(t *testing.T) {
	m := NewModel(makeDeps())
	m, _ = func() (Model, tea.Msg) {
		next, cmd := m.Update(windowMsg())
		return next.(Model), func() tea.Msg {
			if cmd != nil {
				return cmd()
			}
			return nil
		}()
	}()

	// Type "hello" character by character.
	for _, ch := range "hello" {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		next, _ := m.Update(msg)
		m = next.(Model)
	}

	if m.palette.Active() {
		t.Error("palette should not be active after typing regular text")
	}
}

func TestModel_SlashOpens_Palette(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Type "/" — should trigger palette open.
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	next, _ = m.Update(msg)
	m = next.(Model)

	if !m.palette.Active() {
		t.Error("palette should be active after typing '/'")
	}
}

func TestModel_Enter_EmitsSubmitMsg(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Type "hello world".
	for _, ch := range "hello world" {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		next, _ := m.Update(msg)
		m = next.(Model)
	}

	// Press Enter.
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	next, cmd := m.Update(enterMsg)
	m = next.(Model)

	if cmd == nil {
		t.Fatal("Enter should produce a Cmd")
	}
	produced := cmd()
	if produced == nil {
		t.Fatal("Cmd() returned nil msg")
	}
	// The prompt Update emits submitMsg.
	sm, ok := produced.(submitMsg)
	if !ok {
		// Also acceptable: agentDoneMsg (when AgentRunner is nil).
		switch produced.(type) {
		case agentDoneMsg:
			return // acceptable
		}
		t.Fatalf("unexpected msg type %T; want submitMsg or agentDoneMsg", produced)
	}
	if string(sm) != "hello world" {
		t.Errorf("submitMsg = %q, want %q", string(sm), "hello world")
	}
}

func TestModel_Esc_CancelsWithoutQuit(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	// Esc should not quit.
	if cmd != nil {
		msg := cmd()
		if _, isQuit := msg.(tea.QuitMsg); isQuit {
			t.Error("Esc should not quit the program")
		}
	}
	// Model should still be ready.
	if !m.ready {
		t.Error("model should still be ready after Esc")
	}
}

func TestModel_CtrlC_Twice_Quits(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// First Ctrl+C: cancels request.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	_ = cmd

	// Simulate second Ctrl+C within the timeout window.
	m.lastCtrlC = time.Now() // ensure within window
	m.ctrlCCount = 1
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)

	if cmd == nil {
		t.Fatal("second Ctrl+C should produce a Cmd (tea.Quit)")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("second Ctrl+C should produce tea.QuitMsg, got %T", msg)
	}
}

func TestModel_CtrlL_ClearsStream(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Write something to the stream.
	sNext, _ := m.stream.Update(streamTokenMsg("some content"))
	m.stream = sNext

	// Ctrl+L.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(Model)

	// Stream content should be empty.
	if strings.Contains(m.stream.View(), "some content") {
		t.Error("Ctrl+L should clear the stream content")
	}
}

func TestModel_PaletteEsc_Closes(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Open palette.
	m.palette.Open("/")
	if !m.palette.Active() {
		t.Fatal("palette should be open")
	}

	// Esc closes palette.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if m.palette.Active() {
		t.Error("palette should be closed after Esc")
	}
}

func TestModel_HeaderView_ContainsContext(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	view := m.header.View()
	if !strings.Contains(view, "test-ctx") {
		t.Errorf("header view should contain context %q, got %q", "test-ctx", view)
	}
	if !strings.Contains(view, "test-ns") {
		t.Errorf("header view should contain namespace %q, got %q", "test-ns", view)
	}
}

func TestModel_HelpAction_WritesToStream(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "help"})
	if cmd != nil {
		cmd()
	}

	// Stream content should contain "shortcuts".
	if !strings.Contains(m.stream.content.String(), "shortcuts") {
		t.Error("help action should write help text to stream")
	}
}

func TestModel_VersionAction_WritesToStream(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "version"})
	if cmd != nil {
		cmd()
	}

	if !strings.Contains(m.stream.content.String(), "cloudy") {
		t.Error("version action should write version to stream")
	}
}
