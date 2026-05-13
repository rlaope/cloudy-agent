package tools

import (
	"encoding/json"
	"fmt"
	"sort"
)

// MustJSON marshals v as JSON and panics on failure. Used by tool groups to
// produce the json.RawMessage Schema fields from inline map literals; a
// failure is always a programmer error in the schema construction code, so
// panic-at-init is the right behaviour.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("tools: marshal schema: %v", err))
	}
	return b
}

// AsString renders v as a string for table cells: nil → "", []byte → utf-8
// string, anything else → fmt.Sprintf("%v"). Shared by tool wrappers that
// project driver values or JSON map values into a render.Table row.
func AsString(v any) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// PickEndpoint resolves a per-call endpoint argument against the map of
// configured clients for one tool-group + backend kind. The rules are
// shared across every HTTP- or driver-backed tool that supports multiple
// named endpoints:
//
//   - If name is empty AND the map has exactly one entry, return that one.
//   - If name matches an entry, return it.
//   - Otherwise, return an error that lists the configured names so the LLM
//     can self-correct on the next call.
//
// group is the tool-group prefix ("db", "log", "trace", "perf"); kind is
// the backend within that group ("redis endpoint", "loki endpoint", etc.).
func PickEndpoint[V any](m map[string]V, name, group, kind string) (V, error) {
	var zero V
	if name == "" {
		if len(m) == 1 {
			for _, v := range m {
				return v, nil
			}
		}
		return zero, fmt.Errorf("%s: %s name required (configured: %s)", group, kind, joinKeys(m))
	}
	v, ok := m[name]
	if !ok {
		return zero, fmt.Errorf("%s: unknown %s %q (configured: %s)", group, kind, name, joinKeys(m))
	}
	return v, nil
}

// joinKeys returns the keys of m as a comma-separated, sorted string. Sorted
// to keep error messages stable across calls.
func joinKeys[V any](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}

// Schema is a tiny builder for the {"type":"object", "properties": …, "required": …}
// JSON Schema shape that every tool emits. It returns json.RawMessage ready
// to assign to Spec.Schema.
func Schema(properties map[string]any, required []string) json.RawMessage {
	obj := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		obj["required"] = required
	}
	return MustJSON(obj)
}

// StrProp returns a JSON Schema property fragment for a string field.
func StrProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// IntProp returns a JSON Schema property fragment for an integer field.
func IntProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// BoolProp returns a JSON Schema property fragment for a boolean field.
func BoolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
