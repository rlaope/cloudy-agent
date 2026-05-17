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
