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
	if r2.picker.items[0].key != "claude-opus-4-7" {
		t.Errorf("model picker should default to Opus 4.7 first, got %q",
			r2.picker.items[0].key)
	}

	r3 := chat.Step("claude-opus-4-7")
	if !r3.done {
		t.Errorf("step 3 (model pick) should finish, got: %q", r3.out)
	}
	if r3.swapToModel != "claude-opus-4-7" {
		t.Errorf("swapToModel = %q, want claude-opus-4-7", r3.swapToModel)
	}
}
