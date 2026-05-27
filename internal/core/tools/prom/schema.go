package prom

import "encoding/json"

// schema builds a minimal JSON Schema object for tool argument validation.
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
		panic("prom: schema marshal: " + err.Error())
	}
	return b
}

func strProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func strArrayProp(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": description,
	}
}
