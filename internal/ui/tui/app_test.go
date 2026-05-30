package tui

import (
	"context"
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

	// Help text is printed into native scrollback via tea.Println.
	if !strings.Contains(printedText(cmd), "shortcuts") {
		t.Error("help action should write help text to scrollback")
	}
}

func TestModel_VersionAction_WritesToStream(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "version"})

	if !strings.Contains(printedText(cmd), "cloudy") {
		t.Error("version action should write version to scrollback")
	}
}

// --- /scope tests ---

func TestModel_ScopeCmd_SetsScope(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Simulate submitting "/scope ns=payments".
	cmd := m.handleScopeCmd("ns=payments")
	out := printedText(cmd)

	sc := m.currentScope()
	if len(sc.Namespaces) != 1 || sc.Namespaces[0] != "payments" {
		t.Errorf("scope.Namespaces = %v, want [payments]", sc.Namespaces)
	}
	if !strings.Contains(out, "payments") {
		t.Error("scope confirmation should mention the namespace in scrollback output")
	}
}

func TestModel_ScopeCmd_Reset_ClearsScope(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Set a scope first.
	m.handleScopeCmd("ns=payments")
	// Reset it.
	cmd := m.handleScopeCmd("reset")
	out := printedText(cmd)

	sc := m.currentScope()
	if !sc.Empty() {
		t.Errorf("scope should be empty after reset, got %+v", sc)
	}
	if !strings.Contains(out, "reset") {
		t.Error("scrollback should contain reset confirmation")
	}
}

func TestModel_ScopeCmd_MultipleNamespaces(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handleScopeCmd("ns=payments,checkout")
	if cmd != nil {
		cmd()
	}

	sc := m.currentScope()
	if len(sc.Namespaces) != 2 {
		t.Errorf("expected 2 namespaces, got %v", sc.Namespaces)
	}
}

func TestModel_ScopeCmd_ContextKey(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handleScopeCmd("ctx=prod-eu")
	if cmd != nil {
		cmd()
	}

	sc := m.currentScope()
	if len(sc.Contexts) != 1 || sc.Contexts[0] != "prod-eu" {
		t.Errorf("scope.Contexts = %v, want [prod-eu]", sc.Contexts)
	}
}

func TestModel_ScopeCmd_InvalidArg_EmitsError(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handleScopeCmd("badkey=foo")

	if !strings.Contains(printedText(cmd), "scope error") {
		t.Error("invalid scope key should emit error to scrollback")
	}
	// Scope should not change.
	if !m.currentScope().Empty() {
		t.Error("scope should remain empty after parse error")
	}
}

func TestModel_PaletteIncludes_Scope(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)
	_ = m

	found := false
	for _, item := range builtinItems {
		if item.title == "scope" {
			found = true
			break
		}
	}
	if !found {
		t.Error("palette builtinItems should include a 'scope' item")
	}
}

// TestModel_PaletteScope_BareTriggersPicker pins the HITL flow: a
// bare /scope from the palette must clear the prompt and arm the
// namespace picker, rather than the old "insert /scope prefix and
// make the operator type the arg" stub. The async kubectl call is
// triggered as a tea.Cmd; we don't invoke it here (tests don't need a
// live cluster), we only check that scopePickerActive flipped.
func TestModel_PaletteScope_BareTriggersPicker(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "scope"})
	if cmd == nil {
		t.Fatal("bare /scope should return a cmd (kubectl + chrome write)")
	}
	if !m.scopePickerActive {
		t.Error("bare /scope must arm scopePickerActive so the result lands in the picker")
	}
	if m.prompt.Value() != "" {
		t.Errorf("bare /scope should clear the prompt; got %q", m.prompt.Value())
	}
}

// TestModel_PaletteScope_WithArgRoutesToHandler confirms that the
// scope action still honours the parsed-arg path when the operator
// typed e.g. "/scope ns=foo" — that goes straight to handleScopeCmd
// instead of the picker.
func TestModel_PaletteScope_WithArgRoutesToHandler(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "scope", arg: "ns=payments"})
	if m.scopePickerActive {
		t.Error("/scope with an arg must not arm the picker")
	}
	if got := m.scope.Namespaces; len(got) != 1 || got[0] != "payments" {
		t.Errorf("/scope ns=payments should apply the namespace; got %v", got)
	}
}

// --- agentUsageMsg / cost meter tests ---

func TestModel_AgentUsageMsg_AccumulatesUsage(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Send first usage message.
	next, cmd := m.Update(agentUsageMsg{Input: 100, Output: 50, USD: 0.006})
	m = next.(Model)
	if cmd != nil {
		cmd()
	}

	if m.usage.Input != 100 {
		t.Errorf("usage.Input = %d, want 100", m.usage.Input)
	}
	if m.usage.Output != 50 {
		t.Errorf("usage.Output = %d, want 50", m.usage.Output)
	}
	if m.usage.USD != 0.006 {
		t.Errorf("usage.USD = %f, want 0.006", m.usage.USD)
	}

	// Send a second usage message — should accumulate.
	next, cmd = m.Update(agentUsageMsg{Input: 200, Output: 100, USD: 0.006})
	m = next.(Model)
	if cmd != nil {
		cmd()
	}

	if m.usage.Input != 300 {
		t.Errorf("usage.Input = %d, want 300 (accumulated)", m.usage.Input)
	}
	if m.usage.USD != 0.012 {
		t.Errorf("usage.USD = %f, want 0.012 (accumulated)", m.usage.USD)
	}
}

func TestModel_AgentUsageMsg_UpdatesHeader(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, cmd := m.Update(agentUsageMsg{Input: 1000, Output: 500, USD: 0.012})
	m = next.(Model)
	if cmd != nil {
		cmd()
	}

	view := m.header.View()
	if !strings.Contains(view, "0.012") {
		t.Errorf("header view should contain cost '0.012', got: %q", view)
	}
}

// --- /setup mode tests ---

// TestWelcome_VisibleOnEmptyStream verifies that the initial View() rendered
// on an empty stream contains the cloudy banner and the /setup hint.
func TestWelcome_VisibleOnEmptyStream(t *testing.T) {
	deps := makeDeps()
	deps.FirstRun = true
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	view := m.View()
	if !strings.Contains(view, "/setup") {
		t.Errorf("welcome should contain '/setup' on empty stream, got: %q", view)
	}
	if !strings.Contains(view, "cloudy") {
		t.Errorf("welcome should contain 'cloudy' banner on empty stream, got: %q", view)
	}
}

// TestSetupSubmit_EntersInlineChat drives the model through a /setup
// submit and confirms either the inline conversation starts (when a
// kubeconfig is available) or the no-kubeconfig error is written to the
// stream. Both paths are valid for environments where the test runs.
func TestSetupSubmit_EntersInlineChat(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	next, cmd := m.Update(submitMsg("/setup"))
	m = next.(Model)

	// firstPrintln runs only the chrome print, never the async scan cmd
	// the greeting path batches after it.
	out := firstPrintln(cmd)
	hasGreeting := strings.Contains(out, "--- /setup")
	hasNoKubeconfig := strings.Contains(out, "no kubeconfig contexts")
	if !hasGreeting && !hasNoKubeconfig {
		t.Errorf("/setup should emit a greeting or no-kubeconfig error, got: %q", out)
	}
	if hasGreeting && m.setupChat == nil {
		t.Error("greeting was emitted but setupChat is nil")
	}
}

// TestSetupPaletteAction_EntersSetup confirms that selecting the "setup"
// palette item routes through the same enterSetup path.
func TestSetupPaletteAction_EntersSetup(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "setup"})

	out := firstPrintln(cmd)
	if !strings.Contains(out, "--- /setup") && !strings.Contains(out, "no kubeconfig contexts") {
		t.Errorf("palette setup action should emit a greeting or no-kubeconfig error, got: %q", out)
	}
}

// TestPaletteIncludes_Setup verifies the builtin palette items list contains
// the "setup" entry so Tab-completion can pick it up.
func TestPaletteIncludes_Setup(t *testing.T) {
	found := false
	for _, item := range builtinItems {
		if item.title == "setup" {
			found = true
			break
		}
	}
	if !found {
		t.Error("palette builtinItems should include a 'setup' item")
	}
}

// TestPaletteIncludes_NewCommands verifies the new v0.4-UX commands
// (/exit, /update) are registered so palette tab-completion can pick
// them up alongside the original /quit, /setup, etc.
//
// `/set-up` is intentionally NOT listed — it remains accepted by the
// dispatcher as a typo-tolerant alias (covered by
// TestPaletteAction_SetUpAlias_EntersSetup) but is no longer surfaced
// in the suggestion list to keep the palette clean.
func TestPaletteIncludes_NewCommands(t *testing.T) {
	want := map[string]bool{"exit": false, "update": false}
	for _, item := range builtinItems {
		if _, ok := want[item.title]; ok {
			want[item.title] = true
		}
	}
	for cmd, present := range want {
		if !present {
			t.Errorf("builtinItems missing %q", cmd)
		}
	}
}

// TestPaletteAction_Plan_Toggles confirms /plan flips Deps.TogglePlan and
// reports the new state, and that a nil TogglePlan degrades to an
// "unavailable" line rather than panicking.
func TestPaletteAction_Plan_Toggles(t *testing.T) {
	on := true
	deps := makeDeps()
	deps.TogglePlan = func() bool { on = !on; return on }

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// First toggle: true → false.
	if cmd := m.handlePaletteAction(paletteActionMsg{cmd: "plan"}); cmd == nil {
		t.Fatal("plan action should return a writeStream command")
	}
	if on {
		t.Errorf("TogglePlan not invoked; on = %v, want false", on)
	}
	// Second toggle: false → true.
	m.handlePaletteAction(paletteActionMsg{cmd: "plan"})
	if !on {
		t.Errorf("second toggle did not flip back; on = %v, want true", on)
	}

	// Nil guard: no closure wired → graceful "unavailable", no panic.
	m2 := NewModel(makeDeps())
	next2, _ := m2.Update(windowMsg())
	m2 = next2.(Model)
	if cmd := m2.handlePaletteAction(paletteActionMsg{cmd: "plan"}); cmd == nil {
		t.Fatal("plan action with nil TogglePlan should still return a writeStream command")
	}
}

// TestPaletteAction_AutoCompact_Toggles confirms /autocompact flips the Model
// flag and that the trigger only fires past the threshold when enabled.
func TestPaletteAction_AutoCompact_Toggles(t *testing.T) {
	deps := makeDeps()
	deps.CompactHistory = func(context.Context) (string, error) { return "summary", nil }
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	if m.autoCompact {
		t.Fatal("auto-compact must default to off")
	}
	if cmd := m.handlePaletteAction(paletteActionMsg{cmd: "autocompact"}); cmd == nil {
		t.Fatal("autocompact action should return a writeStream command")
	}
	if !m.autoCompact {
		t.Fatal("autocompact toggle did not enable the flag")
	}

	// model context window is 128000 (default). 120000 input ≈ 93% ≥ 90 → fires.
	m.usage.LastInputTokens = 120000
	if m.maybeAutoCompactCmd() == nil {
		t.Error("expected auto-compact to fire at 93% with the flag on")
	}
	// 100000 ≈ 78% < 90 → no fire.
	m.usage.LastInputTokens = 100000
	if m.maybeAutoCompactCmd() != nil {
		t.Error("auto-compact must not fire below the threshold")
	}
	// Disabled → never fires even when over threshold.
	m.autoCompact = false
	m.usage.LastInputTokens = 127000
	if m.maybeAutoCompactCmd() != nil {
		t.Error("auto-compact must not fire when disabled")
	}
}

// TestPaletteAction_Exit_Quits confirms the /exit alias produces a tea.Quit
// command, matching the /quit behaviour the user already relied on.
func TestPaletteAction_Exit_Quits(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "exit"})
	if cmd == nil {
		t.Fatal("exit action should return a tea.Quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("exit action should produce tea.QuitMsg, got %T", cmd())
	}
}

// TestPaletteAction_Update_KicksSelfUpdate confirms /update writes the
// "checking for cloudy update…" intro line synchronously AND returns a
// non-nil cmd that will fan out to the selfupdate goroutine. We do NOT
// execute the returned cmd: that would hit the real GitHub API and is
// not appropriate for a unit test.
func TestPaletteAction_Update_KicksSelfUpdate(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "update"})

	// firstPrintln runs only the chrome print, never the GitHub self-update
	// fetch the handler batches after it.
	out := firstPrintln(cmd)
	if !strings.Contains(out, "checking for cloudy update") {
		t.Errorf("update action should print the intro line to scrollback; got %q", out)
	}
	if cmd == nil {
		t.Error("update action should return a non-nil cmd that kicks the selfupdate goroutine")
	}
}

// TestWriteStream_TwoCallsNoPanic regresses the strings.Builder copy
// panic that hit /login: StreamModel.Update has a value receiver, so a
// non-zero strings.Builder was copied and Go's runtime rejected the
// second write. Holding the Builder behind a pointer fixes it; two
// back-to-back writeStream calls must run cleanly through stream.Update
// without recovering from a panic.
func TestWriteStream_TwoCallsNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("writeStream panicked on second call: %v", r)
		}
	}()
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	out1 := printedText(m.writeStream("first\n"))
	out2 := printedText(m.writeStream("second\n"))
	if !strings.Contains(out1, "first") || !strings.Contains(out2, "second") {
		t.Errorf("scrollback missing one of the writes: %q / %q", out1, out2)
	}
}

// TestPaletteAction_SetUpAlias_EntersSetup confirms /set-up routes to the
// same wizard entry point as /setup.
func TestPaletteAction_SetUpAlias_EntersSetup(t *testing.T) {
	m := NewModel(makeDeps())
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "set-up"})

	out := firstPrintln(cmd)
	if !strings.Contains(out, "--- /setup") && !strings.Contains(out, "no kubeconfig contexts") {
		t.Errorf("set-up alias should emit a greeting or no-kubeconfig error, got: %q", out)
	}
}
