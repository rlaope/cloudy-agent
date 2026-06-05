package tui

import (
	"strings"
	"testing"
)

// TestLoginChat_Greeting_ProvidesArrowPicker confirms newLoginChat
// returns a picker spec instead of dumping options into the greeting.
func TestLoginChat_Greeting_ProvidesArrowPicker(t *testing.T) {
	_, res := newLoginChat()
	if res.picker == nil {
		t.Fatal("newLoginChat should return an arrow picker for provider selection")
	}
	if len(res.picker.items) != len(loginProviders) {
		t.Errorf("picker should have %d items, got %d", len(loginProviders), len(res.picker.items))
	}
	if res.picker.items[0].key != "anthropic" {
		t.Errorf("first picker item should be anthropic, got %q", res.picker.items[0].key)
	}
	if !strings.Contains(res.out, "/login") {
		t.Errorf("greeting should announce /login, got: %q", res.out)
	}
}

// TestLoginChat_NumberSelection drives Step with a numeric input and
// confirms the conversation advances to the key-prompt step. Number
// selection is retained as a typed fallback for terminals where the
// arrow picker can't render (NO_COLOR, dumb TTYs, etc.).
func TestLoginChat_NumberSelection(t *testing.T) {
	chat, _ := newLoginChat()
	res := chat.Step("2")
	if res.done {
		t.Error("step 0 with valid number should not be done")
	}
	if !strings.Contains(res.out, "OPENAI_API_KEY") {
		t.Errorf("step 0 should advance to openai key prompt, got: %q", res.out)
	}
	if chat.provider != "openai" {
		t.Errorf("provider should be 'openai', got %q", chat.provider)
	}
}

// TestLoginChat_NameSelection drives Step with a provider name and
// confirms it resolves to the same advance state.
func TestLoginChat_NameSelection(t *testing.T) {
	chat, _ := newLoginChat()
	res := chat.Step("google")
	if res.done {
		t.Error("step 0 with valid name should not be done")
	}
	if !strings.Contains(res.out, "GOOGLE_API_KEY") {
		t.Errorf("step 0 should advance to google key prompt, got: %q", res.out)
	}
}

func TestLoginChat_CodexSelection(t *testing.T) {
	chat, _ := newLoginChat()
	res := chat.Step("codex")
	if res.done {
		t.Error("step 0 with codex should not be done")
	}
	if !strings.Contains(res.out, "CODEX_API_KEY") {
		t.Errorf("step 0 should advance to codex key prompt, got: %q", res.out)
	}
	if chat.provider != "codex" {
		t.Errorf("provider should be 'codex', got %q", chat.provider)
	}
}

// TestLoginChat_BadInput_Retries confirms unknown numbers and names
// keep the conversation on step 0 with a helpful retry message.
func TestLoginChat_BadInput_Retries(t *testing.T) {
	chat, _ := newLoginChat()
	res := chat.Step("99")
	if res.done {
		t.Error("bad input should not finish the conversation")
	}
	if !strings.Contains(res.out, "unknown provider") {
		t.Errorf("bad input should print 'unknown provider', got: %q", res.out)
	}
	if chat.step != 0 {
		t.Errorf("step should stay at 0 on bad input, got %d", chat.step)
	}
}

// TestLoginChat_Cancel aborts cleanly at any step.
func TestLoginChat_Cancel(t *testing.T) {
	chat, _ := newLoginChat()
	res := chat.Step("cancel")
	if !res.done {
		t.Error("cancel should finish the conversation")
	}
	if !strings.Contains(res.out, "cancelled") {
		t.Errorf("cancel should print cancellation message, got: %q", res.out)
	}
}

// TestLoginChat_ModelPicker_RejectsUnknownId guards against the
// operator (or an upstream bug) sending an id that isn't in the
// provider's curated list. Step 3 must re-issue the picker with a
// helpful error instead of silently calling SwapModel on garbage —
// SwapModel would then fail with "unknown model" from wiring and the
// chat would end in a confusing state.
func TestLoginChat_ModelPicker_RejectsUnknownId(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	chat, _ := newLoginChat()
	chat.Step("anthropic")
	chat.Step("sk-test-not-real")
	// Chat is now at step 3 with anthropic's curated model list.

	res := chat.Step("claude-9000-imaginary")
	if res.done {
		t.Error("unknown model id must not finish the chat")
	}
	if res.swapToModel != "" {
		t.Errorf("unknown model id must not produce swapToModel, got %q", res.swapToModel)
	}
	if res.picker == nil {
		t.Error("unknown model id must re-issue the picker so the operator can retry")
	}
	if !strings.Contains(res.out, "unknown model") {
		t.Errorf("operator should see 'unknown model' nudge, got: %q", res.out)
	}
}

// TestLoginChat_FullThreeStepFlow walks the happy path end-to-end:
// provider pick → key save → model pick. The third step is the new
// one; without it /login would auto-pick a hard-coded "suggested"
// model that may already be deprecated (this was the actual bug —
// claude-3-5-sonnet-20241022 started 404ing).
func TestLoginChat_FullThreeStepFlow(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	chat, greet := newLoginChat()
	if greet.picker == nil {
		t.Fatal("step 0 must return provider picker")
	}

	r1 := chat.Step("anthropic")
	if r1.picker != nil {
		t.Errorf("step 1 (key prompt) should be text-input only, got picker")
	}
	if !strings.Contains(r1.out, "ANTHROPIC_API_KEY") {
		t.Errorf("step 1 should announce env var, got: %q", r1.out)
	}

	r2 := chat.Step("sk-fake-test-key")
	if r2.done {
		t.Error("step 2 (key save) should not finish — model picker still pending")
	}
	if r2.picker == nil {
		t.Fatal("step 2 must return the model picker")
	}
	if r2.picker.items[0].key != "claude-opus-4-8" {
		t.Errorf("model picker should default to Opus 4.8 first, got %q",
			r2.picker.items[0].key)
	}

	r3 := chat.Step("claude-opus-4-8")
	if !r3.done {
		t.Errorf("step 3 (model pick) should finish, got: %q", r3.out)
	}
	if r3.swapToModel != "claude-opus-4-8" {
		t.Errorf("swapToModel = %q, want claude-opus-4-8", r3.swapToModel)
	}
}

// TestLoginProviders_AllHaveModels guards against a future drive-by
// edit accidentally shipping a provider entry with an empty model
// list — that would leave the step-3 picker with zero rows and the
// operator with no way to finish /login except cancel.
func TestLoginProviders_AllHaveModels(t *testing.T) {
	if len(loginProviders) == 0 {
		t.Fatal("loginProviders is empty — no providers wired up")
	}
	for _, p := range loginProviders {
		if len(p.models) == 0 {
			t.Errorf("provider %q has no curated models — picker would be empty", p.key)
			continue
		}
		if p.envVar == "" {
			t.Errorf("provider %q has no envVar — secrets.Add would fail", p.key)
		}
		seen := map[string]bool{}
		for _, m := range p.models {
			if m.id == "" {
				t.Errorf("provider %q has an empty model id", p.key)
			}
			if seen[m.id] {
				t.Errorf("provider %q has duplicate model id %q", p.key, m.id)
			}
			seen[m.id] = true
		}
	}
}

// TestLoginChat_FullFlow_AllProviders is the table-driven counterpart
// to TestLoginChat_FullThreeStepFlow — runs the same provider → key
// → first-model walk for every provider in loginProviders. Catches
// per-provider regressions that an anthropic-only test would miss
// (e.g. google's first-suggested id getting deprecated, or moonshot's
// envVar drifting).
//
// API roundtrip is not exercised — that's intentional: real LLM calls
// from CI would burn money and leak the test runner's outbound IP.
// The wire-format sanitization regression for hosted providers is
// covered separately in internal/wiring/tools_anthropic_safenames_test.go.
func TestLoginChat_FullFlow_AllProviders(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	for _, p := range loginProviders {
		t.Run(p.key, func(t *testing.T) {
			chat, _ := newLoginChat()

			// Step 1 → provider pick advances to key prompt.
			r1 := chat.Step(p.key)
			if r1.done {
				t.Fatalf("provider pick should not finish chat: %q", r1.out)
			}
			if !strings.Contains(r1.out, p.envVar) {
				t.Errorf("key prompt should mention env var %s, got: %q", p.envVar, r1.out)
			}

			// Step 2 → key save advances to model picker.
			r2 := chat.Step("sk-fake-" + p.key)
			if r2.done {
				t.Fatalf("key save should not finish chat: %q", r2.out)
			}
			if r2.picker == nil {
				t.Fatal("key save must return the model picker")
			}
			wantFirst := p.models[0].id
			if r2.picker.items[0].key != wantFirst {
				t.Errorf("model picker first row = %q, want %q (curated default)",
					r2.picker.items[0].key, wantFirst)
			}
			if len(r2.picker.items) != len(p.models) {
				t.Errorf("model picker has %d items, want %d (matches curated list)",
					len(r2.picker.items), len(p.models))
			}

			// Step 3 → picking the default finishes with swapToModel set.
			r3 := chat.Step(wantFirst)
			if !r3.done {
				t.Errorf("model pick should finish, got: %q", r3.out)
			}
			if r3.swapToModel != wantFirst {
				t.Errorf("swapToModel = %q, want %q", r3.swapToModel, wantFirst)
			}
		})
	}
}
