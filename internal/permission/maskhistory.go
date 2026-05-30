package permission

import "github.com/rlaope/cloudy/internal/core/llm"

// MaskHistory returns a deep-redacted copy of msgs suitable for writing to
// disk (e.g. session resume snapshots). It NEVER mutates the input slice, so
// the live conversation the agent keeps using stays model-faithful.
//
// SECURITY: the in-memory history is not reliably masked — tool observations
// are redacted only when a masking profile is active, and the raw user prompt
// plus assistant prose are never touched by the model-facing pipeline.
// Persisting it verbatim would leak secrets. This pass redacts every
// message's text (MaskString) and every tool-call argument blob (MaskJSON,
// which also catches key-named secrets) before it can reach disk.
//
// When the active profile carries no masking patterns, it falls back to
// DefaultMaskingPatterns() so the on-disk snapshot is never LESS redacted
// than a configured pipeline would produce. Masking is best-effort
// (regex-based): a novel secret shape can still slip through, exactly as the
// production MaskingHook documents.
func MaskHistory(p *Profile, msgs []llm.Message) []llm.Message {
	masker := MaskerOrDefault(p)
	out := cloneHistory(msgs)
	for i := range out {
		// Tool-result bodies are usually JSON blobs carrying key-named
		// secrets (a db row's `password` column, an env dump's `API_KEY`).
		// MaskJSON applies the KeyRegex set too (it no-ops on non-JSON), so
		// run it first for RoleTool Content; MaskString (value patterns)
		// then covers everything, prose included.
		if out[i].Role == llm.RoleTool {
			if masked, err := masker.MaskJSON([]byte(out[i].Content)); err == nil {
				out[i].Content = string(masked)
			}
		}
		out[i].Content = masker.MaskString(out[i].Content)
		for j := range out[i].ToolCalls {
			if masked, err := masker.MaskJSON(out[i].ToolCalls[j].Arguments); err == nil {
				out[i].ToolCalls[j].Arguments = masked
			}
		}
	}
	return out
}

// MaskerOrDefault builds a Masker from the profile's patterns, falling back
// to the built-in baseline when the profile configures no masking (or fails
// to compile) so nothing sensitive is ever written unredacted. It never
// returns nil, which makes it safe for seams that must redact before a disk
// write (e.g. the TUI session-log sink) even when no profile is active.
func MaskerOrDefault(p *Profile) *Masker {
	if m, err := NewMasker(p); err == nil && m != nil {
		return m
	}
	m, _ := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	return m
}

// cloneHistory deep-copies the message slice, including each message's
// tool-call argument bytes, so redaction never aliases the live history.
func cloneHistory(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		c := m
		if len(m.ToolCalls) > 0 {
			c.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				c.ToolCalls[j] = tc
				if len(tc.Arguments) > 0 {
					args := make([]byte, len(tc.Arguments))
					copy(args, tc.Arguments)
					c.ToolCalls[j].Arguments = args
				}
			}
		}
		out[i] = c
	}
	return out
}
