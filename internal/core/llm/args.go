package llm

import (
	"bytes"
	"encoding/json"
)

// NormalizeArguments returns a JSON object suitable for the tool-call
// `arguments` / `input` / `args` field of any provider's wire format.
//
// Anthropic, OpenAI, Codex, Moonshot, OpenAI-compatible and Google all require
// this field to be a JSON object (an empty object is fine; nil / "null" / a
// non-object literal is not). Providers historically serialized whatever the
// model emitted, so a parameter-less tool call (e.g. `k8s_list_nodes`) would
// round-trip as `arguments: ""`, `input: null`, or — with `omitempty` — get
// dropped from the wire entirely, and the next turn 4xx'd with shape errors
// like `messages.<n>.content.<i>.tool_use.input: Field required`.
//
// The collapse rules:
//
//   - nil / empty slice                            → "{}"
//   - JSON `null` (4 bytes, non-empty)             → "{}"
//   - whitespace-only payloads                     → "{}"
//   - partial / invalid JSON                       → "{}"
//   - non-object literals (numbers, strings, …)    → "{}"
//   - already-valid JSON objects                   → returned unchanged
//
// Already-valid objects are returned by reference; callers must not mutate.
func NormalizeArguments(raw json.RawMessage) json.RawMessage {
	const empty = "{}"
	if len(raw) == 0 {
		return json.RawMessage(empty)
	}
	if !json.Valid(raw) {
		return json.RawMessage(empty)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage(empty)
	}
	return raw
}
