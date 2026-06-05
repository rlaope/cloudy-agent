package llm

import "testing"

func TestContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-3-5-sonnet-20241022", 200_000},
		{"claude-opus-4-8", 200_000},
		{"gpt-4o-mini", 128_000},
		{"gpt-4.1", 1_000_000}, // narrower prefix wins over gpt-
		{"codex/gpt-4.1", 1_000_000},
		{"codex/gpt-5.5", 128_000},
		{"o3-mini", 200_000},
		{"gemini-1.5-pro", 1_000_000},
		{"gemini-2.0-flash", 1_000_000},
		{"kimi-k2", 128_000},
		{"moonshot-v1-128k", 128_000},
		{"local/llama3", 32_000},
		{"some-unknown-model", defaultContextWindow}, // fallback
	}
	for _, c := range cases {
		if got := ContextWindow(c.model); got != c.want {
			t.Errorf("ContextWindow(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}
