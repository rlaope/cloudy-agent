package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/wiring"
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
	// a fresh tmpdir we own. With the three-step flow, key save no longer
	// finishes the chat — it advances to the model picker.
	res := chat.Step("AIzaTESTKEY")
	if res.done {
		t.Fatalf("key paste should advance to model picker, not finish: %q", res.out)
	}
	if res.picker == nil {
		t.Fatal("key paste must return the model picker for step 3")
	}
	if !strings.Contains(res.out, "GOOGLE_API_KEY") {
		t.Errorf("save line should mention env var, got: %q", res.out)
	}

	// Step 2: pick a model from the curated list. The id must round-trip
	// into swapToModel so the parent can call Deps.SwapModel.
	res = chat.Step("gemini-2.5-pro")
	if !res.done {
		t.Fatalf("model pick should finish chat, got: %q", res.out)
	}
	if res.swapToModel != "gemini-2.5-pro" {
		t.Errorf("swapToModel = %q, want %q (the picked model id)",
			res.swapToModel, "gemini-2.5-pro")
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

	out := printedText(m.applyLoginResult(loginResult{
		out:         "✓ Saved as GOOGLE_API_KEY\n",
		done:        true,
		swapToModel: "gemini-2.5-flash",
	}))
	if !strings.Contains(out, "GOOGLE_API_KEY") {
		t.Errorf("scrollback should record the success line, got: %q", out)
	}
	if calledWith != "gemini-2.5-flash" {
		t.Errorf("SwapModel called with %q, want %q", calledWith, "gemini-2.5-flash")
	}
	if m.deps.Model != "gemini-2.5-flash" {
		t.Errorf("deps.Model = %q, want %q (post-swap update)",
			m.deps.Model, "gemini-2.5-flash")
	}
}

func TestMakeSwapModel_PersistsRoutingModelForCodex(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	t.Setenv("CODEX_API_KEY", "test-key")

	ref := &providerRef{}
	swap := makeSwapModel(ref, "")
	if err := swap("codex/gpt-5.5"); err != nil {
		t.Fatalf("swap codex model: %v", err)
	}

	provider, runtimeModel := ref.get()
	if provider == nil {
		t.Fatal("providerRef provider is nil after swap")
	}
	if provider.Name() != "codex" {
		t.Errorf("providerRef provider = %q, want codex", provider.Name())
	}
	if runtimeModel != "gpt-5.5" {
		t.Errorf("runtime wire model = %q, want stripped gpt-5.5", runtimeModel)
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if cfg.DefaultModel != "codex/gpt-5.5" {
		t.Fatalf("DefaultModel = %q, want codex/gpt-5.5", cfg.DefaultModel)
	}

	resolvedProvider, resolvedModel, err := wiring.BuildProvider(cfg.DefaultModel)
	if err != nil {
		t.Fatalf("BuildProvider persisted model: %v", err)
	}
	if resolvedProvider.Name() != "codex" {
		t.Errorf("persisted model provider = %q, want codex", resolvedProvider.Name())
	}
	if resolvedModel != "gpt-5.5" {
		t.Errorf("persisted model wire id = %q, want gpt-5.5", resolvedModel)
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

	out := printedText(m.applyLoginResult(loginResult{
		out:         "✓ Saved as GOOGLE_API_KEY\n",
		done:        true,
		swapToModel: "gemini-2.5-flash",
	}))

	if m.deps.Model != "old-model" {
		t.Errorf("deps.Model should stay %q on swap failure, got %q",
			"old-model", m.deps.Model)
	}
	if !strings.Contains(out, "swap") {
		t.Errorf("scrollback should record swap error, got: %q", out)
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
