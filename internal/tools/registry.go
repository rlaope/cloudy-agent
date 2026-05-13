package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"
)

// Registry holds a set of read-only Tools indexed by name.
// The zero value is not usable; construct one via New.
type Registry struct {
	tools map[string]Tool
}

// New returns an empty, ready-to-use Registry.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t to the registry. It panics if:
//   - t.ReadOnly() returns false (safety guard — no mutating tools allowed).
//   - a tool with the same name is already registered.
func (r *Registry) Register(t Tool) {
	if !t.ReadOnly() {
		panic(fmt.Sprintf("tools: tool %q must be read-only (ReadOnly() returned false)", t.Name()))
	}
	if _, exists := r.tools[t.Name()]; exists {
		panic(fmt.Sprintf("tools: tool %q already registered", t.Name()))
	}
	r.tools[t.Name()] = t
}

// MustRegister registers each tool in ts, panicking on any violation.
func (r *Registry) MustRegister(ts ...Tool) {
	for _, t := range ts {
		r.Register(t)
	}
}

// Get returns the tool with the given name and a boolean indicating whether
// it was found.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools in stable alphabetical order by name.
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

// Filter returns a new Registry containing only the tools whose Name()
// matches at least one pattern in allow. Patterns support a trailing
// wildcard '*', e.g. "k8s.*" matches "k8s.list_pods" but not "prom.query".
// An exact match (no wildcard) is also supported.
func (r *Registry) Filter(allow []string) *Registry {
	sub := New()
	for _, t := range r.List() {
		if matchesAny(t.Name(), allow) {
			sub.tools[t.Name()] = t
		}
	}
	return sub
}

// ToolsFor converts the registry contents to llm.Tool descriptors suitable
// for inclusion in an llm.Request. The provider parameter is reserved for
// future per-provider quirks; v1 returns the same list for all providers.
func (r *Registry) ToolsFor(_ string) []llm.Tool {
	list := r.List()
	out := make([]llm.Tool, len(list))
	for i, t := range list {
		out[i] = llm.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		}
	}
	return out
}

// matchesAny reports whether name matches any pattern in patterns.
// Each pattern may optionally end with '*' to match any suffix.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "*") {
			prefix := p[:len(p)-1]
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}
