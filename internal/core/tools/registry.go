package tools

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/registry"
)

// ErrMutatorTool is the panic value raised by Register when a tool's name
// contains a forbidden mutator token. Use errors.Is on the recovered value.
var ErrMutatorTool = errors.New("tools: mutator tool rejected by read-only registry")

// mutatorTokens are verbs whose presence in a tool's dotted/underscored name
// signals a write operation. cloudy is read-only at the contract level, so
// adding a tool whose name contains any of these is rejected at registration
// rather than waiting until the LLM tries to call it.
//
// Detection is token-aware (split on '.' and '_') so legitimate names like
// "mysql_top_table_size" are not flagged for containing "set" as a substring.
var mutatorTokens = map[string]struct{}{
	// Original block — HTTP verbs + common K8s + SQL mutators.
	"create":  {},
	"update":  {},
	"delete":  {},
	"patch":   {},
	"apply":   {},
	"replace": {},
	"drop":    {},
	"alter":   {},
	"insert":  {},
	"kill":    {},
	"restart": {},
	"scale":   {},
	"rollout": {},
	"exec":    {},
	"write":   {},
	"post":    {},
	"put":     {},

	// Added by the v0.5 adversarial security review (M-2). The guard
	// is a name check, not a behaviour check — its only purpose is to
	// surface mistakes at register time. Cheap to extend; the cost of
	// a false positive is a tool rename.
	//
	// Only unambiguous-mutator verbs were added. Verbs that double as
	// read-only nouns (label / annotate / set / start / stop / enable /
	// disable / remove) were deliberately omitted: they false-positive
	// against legitimate read tools (prom.label_values, log.loki_label_*,
	// db.mysql_global_status's GlobalStatusTable struct names, etc.)
	// without buying real safety — a mutating tool that uses one of
	// those verbs as its main token would still trip "create"/"update"/
	// "delete" first if it does anything real.
	"recreate":  {},
	"purge":     {},
	"evict":     {},
	"cordon":    {},
	"drain":     {},
	"taint":     {},
	"truncate":  {},
	"terminate": {},
}

// assertReadOnlyName panics with ErrMutatorTool if name contains a forbidden
// mutator verb at a token boundary. It is the gate every Registry.Register
// call passes through.
func assertReadOnlyName(name string) {
	parts := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '.' || r == '_'
	})
	for _, p := range parts {
		if _, bad := mutatorTokens[p]; bad {
			panic(fmt.Errorf("%w: %q contains forbidden token %q — rename to a read-only verb (list/get/show/describe/inspect/query/top)",
				ErrMutatorTool, name, p))
		}
	}
}

// Registry holds a set of read-only Tools indexed by name. It wraps the
// shared generic registry.Map and adds the domain methods the agent and
// skill-filtering pipeline expect (Filter, ToolsFor).
//
// Read-only enforcement is delegated to the transport layer — see the
// package doc on Tool. The zero value is not usable; construct one via New.
//
// A Registry also remembers which tool groups were *skipped* at wire time
// (e.g. "k8s" when no kubeconfig was found) via MarkSkipped, so the /tools
// inventory surface can show why a group is missing instead of silently
// dropping it. Group names are the prefix segment before the first dot in
// a tool name; "k8s.list_pods" belongs to group "k8s".
type Registry struct {
	items *registry.Map[Tool]

	skippedMu sync.RWMutex
	skipped   map[string]string // group → reason

	// llmAliasMu guards llmAlias.
	llmAliasMu sync.RWMutex
	// llmAlias maps a sanitized (LLM-safe) tool name back to its original
	// dotted form. Populated by ToolsFor when a provider's tool-name regex
	// rejects '.' (Anthropic / OpenAI / Codex / Google / Moonshot all do — they
	// require ^[a-zA-Z0-9_-]{1,64}$). The agent's Get() consults this map
	// as a fallback so tool_use calls from the model resolve back to the
	// real tool regardless of which spelling the wire carried.
	llmAlias map[string]string
}

// New returns an empty, ready-to-use Registry.
func New() *Registry {
	return &Registry{
		items:    registry.New[Tool](func(t Tool) string { return t.Name() }),
		skipped:  map[string]string{},
		llmAlias: map[string]string{},
	}
}

// Register adds t to the registry. It panics on duplicate names and on tool
// names that contain a mutator verb (see mutatorTokens).
func (r *Registry) Register(t Tool) {
	assertReadOnlyName(t.Name())
	r.items.MustRegister(t)
}

// MustRegister registers each tool in ts, panicking on any violation.
func (r *Registry) MustRegister(ts ...Tool) {
	for _, t := range ts {
		r.Register(t)
	}
}

// Get returns the tool with the given name and a boolean indicating whether
// it was found. The name lookup is two-stage: first the original (dotted)
// form as registered, then the LLM-safe alias form populated by ToolsFor.
// This lets the agent call Get(tc.Name) with whichever spelling the model
// sent back — providers that don't allow '.' in tool names will receive
// (and echo back) the underscore form, while skill files and operator-
// facing surfaces continue to use the canonical dotted form.
func (r *Registry) Get(name string) (Tool, bool) {
	if t, ok := r.items.Get(name); ok {
		return t, true
	}
	r.llmAliasMu.RLock()
	orig, hit := r.llmAlias[name]
	r.llmAliasMu.RUnlock()
	if !hit {
		return nil, false
	}
	return r.items.Get(orig)
}

// List returns all registered tools in stable alphabetical order by name.
func (r *Registry) List() []Tool { return r.items.All() }

// Filter returns a new Registry containing only the tools whose Name()
// matches at least one pattern in allow. Patterns support a trailing
// wildcard '*', e.g. "k8s.*" matches "k8s.list_pods" but not "prom.query".
// An exact match (no wildcard) is also supported.
//
// Skipped-group reasons and the llmAlias map are carried over from the
// source. Without the alias carry-over a skill-narrowed registry would
// reject inbound tool_use events whose names were sanitized by an earlier
// ToolsFor() call against the parent (e.g. "k8s_list_pods" never resolves
// back to "k8s.list_pods" on the filtered side).
func (r *Registry) Filter(allow []string) *Registry {
	sub := New()
	for _, t := range r.List() {
		if matchesAny(t.Name(), allow) {
			sub.items.MustRegister(t)
		}
	}
	r.skippedMu.RLock()
	for g, reason := range r.skipped {
		sub.skipped[g] = reason
	}
	r.skippedMu.RUnlock()
	r.llmAliasMu.RLock()
	for safe, real := range r.llmAlias {
		sub.llmAlias[safe] = real
	}
	r.llmAliasMu.RUnlock()
	return sub
}

// MarkSkipped records that the tool group named group could not be wired in
// (no binary, unreachable endpoint, missing capability). The reason surfaces
// through Inventory; calling MarkSkipped after Register is a no-op for that
// group, since the group is no longer skipped.
func (r *Registry) MarkSkipped(group, reason string) {
	if group == "" {
		return
	}
	r.skippedMu.Lock()
	defer r.skippedMu.Unlock()
	r.skipped[group] = reason
}

// UnmarkSkipped clears any skip entry for group. It exists for groups whose
// tools come from more than one backend wired in separate RegisterAll passes:
// the first pass may MarkSkipped its backend (e.g. the HTTP "log" group when no
// Loki/ES endpoint is configured), but a later pass can still register a tool
// in that group (the Docker log.container tool). Leaving the stale skip entry
// in place would make the skill validator (which reads Skipped() directly, not
// Inventory()) wrongly suppress references to the now-registered tool.
func (r *Registry) UnmarkSkipped(group string) {
	if group == "" {
		return
	}
	r.skippedMu.Lock()
	defer r.skippedMu.Unlock()
	delete(r.skipped, group)
}

// Skipped returns a copy of the skipped-group reason map.
func (r *Registry) Skipped() map[string]string {
	r.skippedMu.RLock()
	defer r.skippedMu.RUnlock()
	out := make(map[string]string, len(r.skipped))
	for k, v := range r.skipped {
		out[k] = v
	}
	return out
}

// Inventory returns the full per-group registration report — registered
// groups (with their tool names) plus skipped groups (with reasons). Groups
// are sorted by name; tool names within a group are sorted alphabetically.
// A group whose tools were all registered overrides any earlier MarkSkipped
// entry for that group name.
func (r *Registry) Inventory() Inventory {
	byGroup := map[string][]string{}
	for _, t := range r.List() {
		g := groupOf(t.Name())
		byGroup[g] = append(byGroup[g], t.Name())
	}

	groups := make([]GroupReport, 0, len(byGroup)+len(r.skipped))
	for g, names := range byGroup {
		sort.Strings(names)
		groups = append(groups, GroupReport{Name: g, Tools: names})
	}

	r.skippedMu.RLock()
	for g, reason := range r.skipped {
		if _, registered := byGroup[g]; registered {
			continue
		}
		groups = append(groups, GroupReport{Name: g, Skipped: true, Reason: reason})
	}
	r.skippedMu.RUnlock()

	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return Inventory{Groups: groups}
}

// groupOf returns the prefix segment before the first dot in a tool name.
// "k8s.list_pods" → "k8s"; "standalone" → "standalone".
func groupOf(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// ToolsFor converts the registry contents to llm.Tool descriptors suitable
// for inclusion in an llm.Request. When the provider requires LLM-safe
// names (hosted adapters — anthropic, openai, codex, google, moonshot —
// enforce ^[a-zA-Z0-9_-]{1,64}$), '.' in the canonical tool
// name is rewritten to '_' for the wire and a reverse alias is recorded
// so the agent's Get() resolves the tool_use echo back to the real tool.
//
// openai_compat and any "" provider name keep the original spelling
// (Ollama / vLLM may forward to a model that tolerates dots).
func (r *Registry) ToolsFor(provider string) []llm.Tool {
	list := r.List()
	sanitize := providerNeedsSafeNames(provider)
	out := make([]llm.Tool, len(list))
	var aliases map[string]string
	if sanitize {
		aliases = make(map[string]string, len(list))
	}
	for i, t := range list {
		name := t.Name()
		if sanitize {
			safe := strings.ReplaceAll(name, ".", "_")
			if safe != name {
				aliases[safe] = name
			}
			name = safe
		}
		out[i] = llm.Tool{
			Name:        name,
			Description: t.Description(),
			Schema:      t.Schema(),
		}
	}
	if sanitize {
		r.llmAliasMu.Lock()
		// Merge — earlier ToolsFor calls (e.g. before a provider swap)
		// may have populated entries we still want for in-flight lookups.
		for k, v := range aliases {
			r.llmAlias[k] = v
		}
		r.llmAliasMu.Unlock()
	}
	return out
}

// providerNeedsSafeNames reports whether the named LLM provider requires
// tool names to match ^[a-zA-Z0-9_-]+$ (no '.'). Hosted adapters do;
// openai_compat passes through to user-controlled backends so we
// leave names alone there.
func providerNeedsSafeNames(provider string) bool {
	switch provider {
	case "anthropic", "openai", "codex", "google", "moonshot":
		return true
	default:
		return false
	}
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
