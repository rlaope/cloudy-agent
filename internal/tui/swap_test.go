package tui

import (
	"errors"
	"strings"
	"testing"
)

// TestLoginChat_KeySave_ReturnsSwapToModel locks in the contract the
// parent depends on: after the operator pastes a valid API key, the
// returned loginResult MUST carry the suggested model id in
// swapToModel. Without this the parent has no way to hot-swap the
// provider and /login degrades back to the broken "saved key but
// next question still hits Anthropic" experience.
func TestLoginChat_KeySave_ReturnsSwapToModel(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	chat, _ := newLoginChat()
	// Step 0: pick google.
	if res := chat.Step("google"); res.done {
		t.Fatalf("provider pick should not finish chat: %q", res.out)
	}
	// Step 1: paste a key. secrets.Add will succeed because CLOUDY_HOME is
	// a fresh tmpdir we own.
	res := chat.Step("AIzaTESTKEY")
	if !res.done {
		t.Fatalf("key paste should finish chat, got: %q", res.out)
	}
	wantModel := "gemini-2.5-flash"
	if res.swapToModel != wantModel {
		t.Errorf("swapToModel = %q, want %q (the suggested id for google)",
			res.swapToModel, wantModel)
	}
	if !strings.Contains(res.out, "GOOGLE_API_KEY") {
		t.Errorf("success message should mention env var, got: %q", res.out)
	}
}

// TestApplyLoginResult_InvokesSwap drives applyLoginResult with a
// loginResult that carries swapToModel and confirms three things:
//
//   - Deps.SwapModel was called with that model id (recorder).
//   - m.deps.Model now reflects the new model (so the setup-gate and
//     subsequent /model lookups see the swap).
//   - The stream printed the chat's own success line; on swap error
//     it also prints the "[swap: …]" diagnostic.
func TestApplyLoginResult_InvokesSwap(t *testing.T) {
	var calledWith string
	deps := makeDeps()
	deps.Model = "" // simulate fresh user (no model picked yet)
	deps.SwapModel = func(id string) error {
		calledWith = id
		return nil
	}
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.applyLoginResult(loginResult{
		out:         "✓ Saved as GOOGLE_API_KEY\n",
		done:        true,
		swapToModel: "gemini-2.5-flash",
	})
	if !strings.Contains(m.stream.content.String(), "GOOGLE_API_KEY") {
		t.Errorf("stream should record the success line, got: %q",
			m.stream.content.String())
	}
	if calledWith != "gemini-2.5-flash" {
		t.Errorf("SwapModel called with %q, want %q", calledWith, "gemini-2.5-flash")
	}
	if m.deps.Model != "gemini-2.5-flash" {
		t.Errorf("deps.Model = %q, want %q (post-swap update)",
			m.deps.Model, "gemini-2.5-flash")
	}
}

// TestApplyLoginResult_SwapErrorReportedInline checks the error path:
// when SwapModel fails (e.g. unknown model or missing env var), the
// failure surfaces in the stream and m.deps.Model is left untouched so
// the operator can retry. We don't want a silent swap that pretends to
// have worked.
func TestApplyLoginResult_SwapErrorReportedInline(t *testing.T) {
	deps := makeDeps()
	deps.Model = "old-model"
	swapErr := errors.New("missing key")
	deps.SwapModel = func(id string) error { return swapErr }

	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.applyLoginResult(loginResult{
		out:         "✓ Saved as GOOGLE_API_KEY\n",
		done:        true,
		swapToModel: "gemini-2.5-flash",
	})

	if m.deps.Model != "old-model" {
		t.Errorf("deps.Model should stay %q on swap failure, got %q",
			"old-model", m.deps.Model)
	}
	if !strings.Contains(m.stream.content.String(), "swap") {
		t.Errorf("stream should record swap error, got: %q",
			m.stream.content.String())
	}
}

// TestPaletteModel_SwapsProvider drives the /model <id> slash command
// and confirms it calls Deps.SwapModel (not just updates the footer
// label as the old stub did, which left the agent runner pointing at
// the original provider).
func TestPaletteModel_SwapsProvider(t *testing.T) {
	var calledWith string
	deps := makeDeps()
	deps.SwapModel = func(id string) error {
		calledWith = id
		return nil
	}
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "model", arg: "claude-3-opus"})

	if calledWith != "claude-3-opus" {
		t.Errorf("SwapModel called with %q, want %q", calledWith, "claude-3-opus")
	}
	if m.deps.Model != "claude-3-opus" {
		t.Errorf("deps.Model = %q, want %q", m.deps.Model, "claude-3-opus")
	}
}
