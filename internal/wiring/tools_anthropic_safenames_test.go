package wiring

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

// TestToolsFor_AnthropicSafeNames is the regression test for the
// user-reported 400 the operator hit immediately after /login anthropic:
//
//	[error: agent: provider stream error: anthropic: API error 400:
//	 {"type":"error","error":{"type":"invalid_request_error",
//	  "message":"tools.0.custom.name: String should match …"}}]
//
// Anthropic, OpenAI, Google, and Moonshot all require tool names to
// match ^[a-zA-Z0-9_-]{1,64}$ — '.' is not allowed. Every cloudy
// tool is named with a "<group>.<verb>" pattern, so the previous
// pass-through ToolsFor sent invalid names on the wire and the
// model's first turn with tools attached blew up.
//
// This test resolves the same providers /login can pick, hands a
// realistic dotted-name tool registry to ToolsFor, and asserts the
// outbound names are wire-legal. openai_compat is exercised
// separately as the explicit pass-through case (Ollama / vLLM may
// run a model that tolerates dots).
func TestToolsFor_AnthropicSafeNames(t *testing.T) {
	// Set keys so wiring.BuildProvider's env precheck passes — we
	// don't actually call the network.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("GOOGLE_API_KEY", "test-key")
	t.Setenv("MOONSHOT_API_KEY", "test-key")

	reg := tools.New()
	reg.MustRegister(
		stubTool{name: "k8s.list_pods"},
		stubTool{name: "prom.query"},
		stubTool{name: "log.loki_query_range"},
		stubTool{name: "trace.jaeger_services"},
		stubTool{name: "jvm.jcmd_gc"},
		stubTool{name: "db.pg_stat_activity"},
	)

	cases := []struct {
		modelHint string
		// expectSanitized: true means ToolsFor must rewrite '.' to '_'
		// because the hosted adapter would reject dotted names at the wire.
		expectSanitized bool
	}{
		{"claude-3-5-sonnet-20241022", true},
		{"gpt-4o-mini", true},
		{"gemini-2.5-flash", true},
		{"kimi-k2-instruct", true},
		{"local/llama3", false}, // openai_compat — pass-through
	}

	for _, c := range cases {
		t.Run(c.modelHint, func(t *testing.T) {
			prov, _, err := BuildProvider(c.modelHint)
			if err != nil {
				t.Fatalf("BuildProvider(%q): %v", c.modelHint, err)
			}
			llmTools := reg.ToolsFor(prov.Name())
			if len(llmTools) == 0 {
				t.Fatal("ToolsFor returned empty list")
			}
			for _, lt := range llmTools {
				if c.expectSanitized {
					if strings.Contains(lt.Name, ".") {
						t.Errorf("%s tool name %q contains '.' — Anthropic-class "+
							"providers reject ^[a-zA-Z0-9_-]+$ violations at the wire",
							prov.Name(), lt.Name)
					}
				}
			}
			if c.expectSanitized {
				// The agent's dispatch must still resolve the sanitized
				// echo from the model back to the dotted internal tool.
				if _, ok := reg.Get("k8s_list_pods"); !ok {
					t.Error("Get must resolve the sanitized alias for dispatch")
				}
			} else {
				// openai_compat path: the dotted name survives.
				dotted := false
				for _, lt := range llmTools {
					if strings.Contains(lt.Name, ".") {
						dotted = true
						break
					}
				}
				if !dotted {
					t.Error("openai_compat should not sanitize; expected at least one dotted name")
				}
			}
		})
	}
}

// stubTool is a minimal tools.Tool for registry-level tests; the
// llm-facing metadata methods (Name/Description/Schema) are what this
// test exercises, Run is never called.
type stubTool struct {
	name string
}

func (s stubTool) Name() string                                            { return s.name }
func (s stubTool) Description() string                                     { return "stub" }
func (s stubTool) Schema() json.RawMessage                                 { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Run(context.Context, json.RawMessage) (tools.Observation, error) { return tools.Observation{}, nil }
