package llm_test

import (
	"encoding/json"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// TestNormalizeArguments locks the canonicalisation rule that every provider
// adapter now relies on for round-tripping parameter-less tool calls. The
// regression it guards: when this rule weakens, the v0.5 400 class on
// Anthropic (`tool_use.input: Field required`) plus the strict-vLLM and
// Gemini variants come right back.
func TestNormalizeArguments(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{"nil", nil, "{}"},
		{"empty_slice", json.RawMessage{}, "{}"},
		{"empty_object", json.RawMessage(`{}`), "{}"},
		{"json_null", json.RawMessage(`null`), "{}"},
		{"whitespace_only", json.RawMessage("   \t\n"), "{}"},
		{"truncated_object", json.RawMessage(`{"name":`), "{}"},
		{"json_array", json.RawMessage(`[]`), "{}"},
		{"json_string", json.RawMessage(`"hello"`), "{}"},
		{"json_number", json.RawMessage(`42`), "{}"},
		{"json_bool", json.RawMessage(`true`), "{}"},
		{"populated", json.RawMessage(`{"a":1}`), `{"a":1}`},
		{"leading_whitespace_object", json.RawMessage("  {\"a\":1}"), "  {\"a\":1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(llm.NormalizeArguments(tc.in))
			if got != tc.want {
				t.Errorf("NormalizeArguments(%q) = %q, want %q", string(tc.in), got, tc.want)
			}
		})
	}
}
