package tui

import (
	"context"
	"strings"
	"testing"
)

// TestMultiArrowPicker_ToggleAndConfirm exercises the new checkbox
// picker primitives: Space toggles individual rows, the cursor moves
// independently of selection, and SelectedKeys returns picks in
// display order. This is the contract /setup leans on for both pick
// steps; if it breaks, multi-select silently degrades to "Enter
// commits whatever's pre-ticked".
func TestMultiArrowPicker_ToggleAndConfirm(t *testing.T) {
	items := []arrowPickerItem{
		{label: "ctx-a", key: "ctx-a"},
		{label: "ctx-b", key: "ctx-b"},
		{label: "ctx-c", key: "ctx-c"},
	}
	p := newMultiArrowPicker("pick:", items, []string{"ctx-a"})

	if got := p.SelectedKeys(); !equal(got, []string{"ctx-a"}) {
		t.Errorf("pre-select should tick ctx-a only, got %v", got)
	}

	// Move to ctx-b and tick.
	p.MoveDown()
	p.Toggle()
	// Move to ctx-c — do NOT tick.
	p.MoveDown()
	if got := p.SelectedKeys(); !equal(got, []string{"ctx-a", "ctx-b"}) {
		t.Errorf("after toggling ctx-b, expected [ctx-a ctx-b], got %v", got)
	}

	// Un-tick ctx-a by jumping back and toggling.
	p.MoveUp()
	p.MoveUp()
	p.Toggle()
	if got := p.SelectedKeys(); !equal(got, []string{"ctx-b"}) {
		t.Errorf("after un-ticking ctx-a, expected [ctx-b], got %v", got)
	}
}

// TestMultiArrowPicker_ToggleIsNoOpOnSingleSelect guards against
// Space accidentally firing on a single-select picker (e.g. /login)
// — Toggle should silently do nothing so the existing /login picker
// keeps its Enter-only contract.
func TestMultiArrowPicker_ToggleIsNoOpOnSingleSelect(t *testing.T) {
	p := newArrowPicker("login provider:", []arrowPickerItem{
		{label: "anthropic", key: "anthropic"},
	})
	p.Toggle()
	if len(p.SelectedKeys()) != 0 {
		t.Errorf("single-select Toggle must not produce SelectedKeys; got %v",
			p.SelectedKeys())
	}
}

// TestSetupChat_ContextPickerIsMultiSelect locks in that step 1 of
// /setup returns a checkbox picker, not the old typed-numbers flow.
// The user complained that /setup felt like data entry, not HITL;
// this regression test catches any future "type 1,2,3" regression.
func TestSetupChat_ContextPickerIsMultiSelect(t *testing.T) {
	// newSetupChat needs at least one kubeconfig context to install a
	// chat. With an empty path it falls back to clientcmd defaults; in
	// a test environment that usually finds nothing, so the chat is
	// nil. To avoid coupling to the dev's ~/.kube/config we manually
	// build a setupChat with seeded contexts.
	s := &setupChat{
		step:          setupStepCtx,
		ctx:           context.Background(),
		contexts:      []string{"prod-eu", "prod-us", "staging"},
		selectedKinds: map[string]bool{},
	}

	picker := s.buildContextPicker()
	if picker == nil {
		t.Fatal("buildContextPicker returned nil")
	}
	if !picker.multiSelect {
		t.Error("step 1 picker must be multi-select")
	}
	if len(picker.items) != 3 {
		t.Errorf("expected 3 context rows, got %d", len(picker.items))
	}
	// First context pre-ticked so a single-context setup is one Enter.
	if !picker.selected[0] {
		t.Error("first context should be pre-ticked")
	}
	if picker.selected[1] || picker.selected[2] {
		t.Error("only first context should be pre-ticked")
	}
}

// TestSetupChat_ApplyMulti_CtxKicksScan drives the contract that
// app.go relies on: after the operator confirms the context picker,
// ApplyMulti must transition into the scanning state and return a
// non-nil cmd (the scan goroutine). Without this the setup wizard
// silently stalls at step 0.
func TestSetupChat_ApplyMulti_CtxKicksScan(t *testing.T) {
	s := &setupChat{
		step:          setupStepCtx,
		ctx:           context.Background(),
		contexts:      []string{"prod-eu", "prod-us"},
		selectedKinds: map[string]bool{},
	}
	res := s.ApplyMulti([]string{"prod-eu"}, false)
	if res.done {
		t.Error("picking a context should not finish setup")
	}
	if s.step != setupStepScanning {
		t.Errorf("step should advance to setupStepScanning, got %d", s.step)
	}
	if res.cmd == nil {
		t.Error("scanning step must return a cmd to kick the discovery goroutine")
	}
	if !strings.Contains(res.out, "Scanning") {
		t.Errorf("expected 'Scanning' banner, got: %q", res.out)
	}
}

// TestSetupChat_ApplyMulti_EmptyCtx_KeepsPickerActive — operator
// hits Enter with zero contexts ticked. We should NOT silently
// proceed (would scan everything and confuse the user); instead the
// picker is re-installed with a "pick at least one" nudge.
func TestSetupChat_ApplyMulti_EmptyCtx_KeepsPickerActive(t *testing.T) {
	s := &setupChat{
		step:          setupStepCtx,
		ctx:           context.Background(),
		contexts:      []string{"prod-eu"},
		selectedKinds: map[string]bool{},
	}
	res := s.ApplyMulti(nil, false)
	if res.done {
		t.Error("empty multi-select should not finish setup")
	}
	if res.picker == nil {
		t.Error("empty multi-select should re-install the picker")
	}
	if !strings.Contains(res.out, "tick at least one") {
		t.Errorf("expected 'tick at least one' nudge, got: %q", res.out)
	}
}

// TestSetupChat_ApplyMulti_Cancel_EndsCleanly — Esc on a multi-select
// picker fires a cancelled msg. setupChat must finish without crashing
// or silently advancing.
func TestSetupChat_ApplyMulti_Cancel_EndsCleanly(t *testing.T) {
	s := &setupChat{
		step:          setupStepCtx,
		ctx:           context.Background(),
		contexts:      []string{"prod-eu"},
		selectedKinds: map[string]bool{},
	}
	res := s.ApplyMulti(nil, true)
	if !res.done {
		t.Error("cancel should mark chat done")
	}
	if !strings.Contains(res.out, "cancelled") {
		t.Errorf("expected 'cancelled' message, got: %q", res.out)
	}
}

// equal returns true when two string slices have identical contents
// in identical order. Local helper to avoid pulling reflect.DeepEqual
// into the test.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
