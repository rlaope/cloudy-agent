package tui

import (
	"strings"
	"testing"
)

// TestModelPicker_BareSlashOpensPicker confirms that `/model` with no
// arg routes through the picker path rather than the old "usage: /model
// <id>" message. User request: arrow + Enter to switch, no typing.
func TestModelPicker_BareSlashOpensPicker(t *testing.T) {
	deps := makeDeps()
	deps.SwapModel = func(string) error { return nil }
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "model", arg: ""})

	if !m.modelPickerActive {
		t.Error("/model (no arg) must set modelPickerActive")
	}
	if m.arrowPicker == nil {
		t.Fatal("/model (no arg) must install the picker")
	}
	if m.arrowPicker.multiSelect {
		t.Error("model picker must be single-select")
	}
	if len(m.arrowPicker.items) == 0 {
		t.Fatal("model picker must have at least one row")
	}
	// Cross-provider — first row should be Anthropic's curated default
	// because loginProviders[0] is anthropic and its models[0] is
	// claude-opus-4-7.
	if m.arrowPicker.items[0].key != "claude-opus-4-7" {
		t.Errorf("first picker row should be Anthropic's curated default, got %q",
			m.arrowPicker.items[0].key)
	}
}

// TestModelPicker_ResolveCallsSwap confirms that picker confirmation
// routes through applyModelSwap, which in turn calls Deps.SwapModel
// with the picked id. Without this, the picker would be cosmetic.
func TestModelPicker_ResolveCallsSwap(t *testing.T) {
	var called string
	deps := makeDeps()
	deps.SwapModel = func(id string) error {
		called = id
		return nil
	}
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Open the picker.
	_ = m.handlePaletteAction(paletteActionMsg{cmd: "model", arg: ""})

	// Simulate the picker firing its resolve message with the user's
	// pick. The dispatch goes through Update, just like the real key
	// handler would do on Enter.
	next, _ = m.Update(arrowPickerResolveMsg{key: "gemini-2.5-pro"})
	m = next.(Model)

	if called != "gemini-2.5-pro" {
		t.Errorf("SwapModel called with %q, want gemini-2.5-pro", called)
	}
	if m.deps.Model != "gemini-2.5-pro" {
		t.Errorf("deps.Model = %q, want gemini-2.5-pro", m.deps.Model)
	}
	if m.modelPickerActive {
		t.Error("modelPickerActive must reset after resolve")
	}
	if m.arrowPicker != nil {
		t.Error("arrowPicker must clear after resolve")
	}
}

// TestModelPicker_CancelDoesNotSwap — Esc on the picker must NOT call
// SwapModel and must NOT touch deps.Model. Without this guard, hitting
// Esc would silently call SwapModel("") and either error or no-op,
// neither of which the operator expects.
func TestModelPicker_CancelDoesNotSwap(t *testing.T) {
	called := false
	deps := makeDeps()
	deps.Model = "claude-opus-4-7"
	deps.SwapModel = func(string) error {
		called = true
		return nil
	}
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "model", arg: ""})
	next, _ = m.Update(arrowPickerResolveMsg{cancelled: true})
	m = next.(Model)

	if called {
		t.Error("cancelled picker must not call SwapModel")
	}
	if m.deps.Model != "claude-opus-4-7" {
		t.Errorf("deps.Model should stay at original on cancel, got %q", m.deps.Model)
	}
	if m.modelPickerActive {
		t.Error("modelPickerActive must reset on cancel")
	}
	if !strings.Contains(m.stream.content.String(), "cancelled") {
		t.Errorf("stream should announce the cancel, got: %q", m.stream.content.String())
	}
}

// TestModelPicker_ExplicitIdStillWorks — `/model gemini-2.5-flash`
// (with an explicit id) bypasses the picker and swaps directly. This
// is the power-user / scripted path; the picker is for arrow-key
// users. Without this regression test, future picker work could
// accidentally route the explicit path through the picker too.
func TestModelPicker_ExplicitIdStillWorks(t *testing.T) {
	var called string
	deps := makeDeps()
	deps.SwapModel = func(id string) error {
		called = id
		return nil
	}
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "model", arg: "claude-haiku-4-5-20251001"})

	if called != "claude-haiku-4-5-20251001" {
		t.Errorf("explicit /model <id> should still call SwapModel directly, got %q", called)
	}
	if m.modelPickerActive {
		t.Error("explicit id must not open the picker")
	}
	if m.arrowPicker != nil {
		t.Error("explicit id must not leave a picker installed")
	}
}

// TestBuildAllModelsPicker_CoversEveryProvider locks in that the
// cross-provider picker enumerates every (provider, model) pair from
// loginProviders. If a new provider is added without surfacing its
// models in the picker, this catches it.
func TestBuildAllModelsPicker_CoversEveryProvider(t *testing.T) {
	picker := buildAllModelsPicker()

	want := 0
	for _, p := range loginProviders {
		want += len(p.models)
	}
	if len(picker.items) != want {
		t.Errorf("picker has %d rows, want %d (sum of every provider's curated models)",
			len(picker.items), want)
	}

	// Each row's hint must mention its provider so the operator can
	// tell apart e.g. "kimi-k2-instruct" vs an anthropic model just by
	// looking at the hint column.
	for _, it := range picker.items {
		if it.hint == "" {
			t.Errorf("row %q has no hint — operator can't see which provider", it.key)
		}
	}
}
