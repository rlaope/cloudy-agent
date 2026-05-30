package permission

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// TestMaskHistoryDefaultRedactsSecrets verifies that with NO active masking
// profile, MaskHistory still redacts canary secrets via the default patterns
// — closing the gap where a nil masker would persist raw prompts verbatim.
func TestMaskHistoryDefaultRedactsSecrets(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "my key is AKIAIOSFODNN7EXAMPLE please check"},
		{Role: llm.RoleAssistant, Content: "ok", ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "db_query", Arguments: []byte(`{"password":"hunter2","host":"db"}`)},
		}},
	}
	out := MaskHistory(nil, msgs)

	joined := out[0].Content + string(out[1].ToolCalls[0].Arguments)
	if strings.Contains(joined, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not redacted: %q", out[0].Content)
	}
	if strings.Contains(string(out[1].ToolCalls[0].Arguments), "hunter2") {
		t.Errorf("password value not redacted: %s", out[1].ToolCalls[0].Arguments)
	}
	if !strings.Contains(string(out[1].ToolCalls[0].Arguments), redacted) {
		t.Errorf("expected %s marker in masked args: %s", redacted, out[1].ToolCalls[0].Arguments)
	}
}

// TestMaskHistoryDoesNotMutateInput proves the live history slice is never
// touched — the agent must keep using the model-faithful, unredacted copy.
func TestMaskHistoryDoesNotMutateInput(t *testing.T) {
	orig := []llm.Message{
		{Role: llm.RoleUser, Content: "AKIAIOSFODNN7EXAMPLE"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "t", Arguments: []byte(`{"token":"abc"}`)},
		}},
	}
	_ = MaskHistory(nil, orig)
	if orig[0].Content != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("input Content was mutated: %q", orig[0].Content)
	}
	if string(orig[1].ToolCalls[0].Arguments) != `{"token":"abc"}` {
		t.Errorf("input args were mutated: %s", orig[1].ToolCalls[0].Arguments)
	}
}

// TestMaskHistoryRespectsActiveProfile verifies an operator's own value regex
// is applied when a profile configures masking.
func TestMaskHistoryRespectsActiveProfile(t *testing.T) {
	p := &Profile{Masking: Masking{ValueRegex: []string{"CORP-[0-9]+"}}}
	out := MaskHistory(p, []llm.Message{{Role: llm.RoleUser, Content: "ticket CORP-4242 open"}})
	if strings.Contains(out[0].Content, "CORP-4242") {
		t.Errorf("profile value_regex not applied: %q", out[0].Content)
	}
}
