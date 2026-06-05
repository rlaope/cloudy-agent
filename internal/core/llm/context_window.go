package llm

import "strings"

// defaultContextWindow is the input-token capacity assumed for any model
// whose prefix isn't in the table below. 128k is the safe modern floor.
const defaultContextWindow = 128_000

// contextWindows maps model-string prefixes to their input-token context
// window. Ordered longest/most-specific prefix first so a narrow match
// (e.g. "gpt-4.1") wins over a broad one ("gpt-"). Mirrors prefixMap's
// table-driven shape; ContextWindow is a pure lookup with no I/O.
var contextWindows = []struct {
	prefix string
	window int
}{
	{"gpt-4.1", 1_000_000},
	{"gpt-4o", 128_000},
	{"gpt-", 128_000},
	{"o1-", 200_000},
	{"o3", 200_000},
	{"o4", 200_000},
	{"claude-", 200_000},
	{"gemini-1.5", 1_000_000},
	{"gemini-", 1_000_000},
	{"kimi-", 128_000},
	{"moonshot-", 128_000},
	{"local/", 32_000},
}

// ContextWindow returns the input-token capacity for a model id, matching
// by prefix. Unknown models fall back to defaultContextWindow so callers
// (e.g. the TUI's context-usage gauge) always get a usable denominator.
func ContextWindow(model string) int {
	if strings.HasPrefix(model, "codex/") {
		return ContextWindow(strings.TrimPrefix(model, "codex/"))
	}
	for _, entry := range contextWindows {
		if strings.HasPrefix(model, entry.prefix) {
			return entry.window
		}
	}
	return defaultContextWindow
}
