package tui

import (
	"fmt"
	"strings"
)

// Scope holds the session-scoped filtering constraints set via /scope.
// It is informational for v0.2.0: the agent is prompted to honor it, but
// no hard permission enforcement is applied at this layer.
type Scope struct {
	Namespaces []string
	Contexts   []string
}

// Empty reports whether the scope has any active constraints.
func (s Scope) Empty() bool {
	return len(s.Namespaces) == 0 && len(s.Contexts) == 0
}

// String returns a compact human-readable representation, e.g.
// "ns:payments,checkout  ctx:prod-eu".
func (s Scope) String() string {
	var parts []string
	if len(s.Namespaces) > 0 {
		parts = append(parts, "ns:"+strings.Join(s.Namespaces, ","))
	}
	if len(s.Contexts) > 0 {
		parts = append(parts, "ctx:"+strings.Join(s.Contexts, ","))
	}
	return strings.Join(parts, "  ")
}

// SystemPromptAddendum returns the text that is prepended to the agent system
// prompt when a non-empty scope is active.
func (s Scope) SystemPromptAddendum() string {
	if s.Empty() {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("User has scoped this session:")
	if len(s.Namespaces) > 0 {
		sb.WriteString(" namespace=[")
		sb.WriteString(strings.Join(s.Namespaces, ", "))
		sb.WriteString("].")
		sb.WriteString(" Prefer those namespaces in tool calls. Decline other namespaces.")
	}
	if len(s.Contexts) > 0 {
		sb.WriteString(" context=[")
		sb.WriteString(strings.Join(s.Contexts, ", "))
		sb.WriteString("].")
		sb.WriteString(" Prefer those contexts in tool calls. Decline other contexts.")
	}
	return sb.String()
}

// parseScope parses a /scope argument string such as:
//
//	"ns=payments"
//	"ns=payments,checkout"
//	"ctx=prod-eu"
//	"reset"
//
// On "reset" it returns a zero Scope and a nil error.
// On unrecognised input it returns an error.
func parseScope(s string) (Scope, error) {
	s = strings.TrimSpace(s)
	if s == "reset" {
		return Scope{}, nil
	}

	var sc Scope
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return Scope{}, fmt.Errorf("scope: empty argument — use ns=<csv>, ctx=<csv>, or reset")
	}

	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || kv[1] == "" {
			return Scope{}, fmt.Errorf("scope: invalid token %q — expected key=value", part)
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		vals := splitCSV(kv[1])
		switch key {
		case "ns", "namespace", "namespaces":
			sc.Namespaces = append(sc.Namespaces, vals...)
		case "ctx", "context", "contexts":
			sc.Contexts = append(sc.Contexts, vals...)
		default:
			return Scope{}, fmt.Errorf("scope: unknown key %q — use ns or ctx", key)
		}
	}
	return sc, nil
}

// currentScope returns the live scope stored on the model.
func (m *Model) currentScope() Scope {
	return m.scope
}

// splitCSV splits a comma-separated string and trims whitespace from each value.
func splitCSV(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
