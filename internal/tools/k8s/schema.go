package k8s

import "encoding/json"

// schema builds a minimal JSON Schema object for tool argument validation.
// propDefs maps property name to a {"type":"...", "description":"..."} object.
// required lists required property names.
func schema(propDefs map[string]any, required []string) json.RawMessage {
	s := map[string]any{
		"type":       "object",
		"properties": propDefs,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic("k8s: schema marshal: " + err.Error())
	}
	return b
}

func strProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// contextProp returns the standard `context` schema property added to every
// K8s tool so the LLM can override the default kubeconfig context per call.
func contextProp() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "kubeconfig context override; empty = default",
	}
}

// withContextProp returns a copy of props with the standard `context`
// property attached. Used by every K8s tool's Schema() to keep the per-call
// context override consistent across the tool surface.
func withContextProp(props map[string]any) map[string]any {
	out := make(map[string]any, len(props)+1)
	for k, v := range props {
		out[k] = v
	}
	out["context"] = contextProp()
	return out
}
