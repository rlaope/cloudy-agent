package tools

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/registry"
)

// Registry holds a set of read-only Tools indexed by name. It wraps the
// shared generic registry.Map with two domain rules of its own:
//
//   - Register panics on tools whose ReadOnly() returns false. This is the
//     last line of defense before the HTTP/K8s transport guards (which would
//     also block any mutating call).
//   - Filter / ToolsFor expose the tool set in shapes the agent and skill
//     filtering pipeline expect.
//
// The zero value is not usable; construct one via New.
type Registry struct {
	items *registry.Map[Tool]
}

// New returns an empty, ready-to-use Registry.
func New() *Registry {
	return &Registry{
		items: registry.New[Tool](func(t Tool) string { return t.Name() }),
	}
}

// Register adds t to the registry. It panics if:
//   - t.ReadOnly() returns false (safety guard — no mutating tools allowed).
//   - a tool with the same name is already registered.
func (r *Registry) Register(t Tool) {
	if !t.ReadOnly() {
		panic(fmt.Sprintf("tools: tool %q must be read-only (ReadOnly() returned false)", t.Name()))
	}
	r.items.MustRegister(t)
}

// MustRegister registers each tool in ts, panicking on any violation.
func (r *Registry) MustRegister(ts ...Tool) {
	for _, t := range ts {
		r.Register(t)
	}
}

// Get returns the tool with the given name and a boolean indicating whether
// it was found.
func (r *Registry) Get(name string) (Tool, bool) { return r.items.Get(name) }

// List returns all registered tools in stable alphabetical order by name.
func (r *Registry) List() []Tool { return r.items.All() }

// Filter returns a new Registry containing only the tools whose Name()
// matches at least one pattern in allow. Patterns support a trailing
// wildcard '*', e.g. "k8s.*" matches "k8s.list_pods" but not "prom.query".
// An exact match (no wildcard) is also supported.
func (r *Registry) Filter(allow []string) *Registry {
	sub := New()
	for _, t := range r.List() {
		if matchesAny(t.Name(), allow) {
			sub.items.MustRegister(t)
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
