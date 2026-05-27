package permission

import (
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// FilterRegistry returns a new *tools.Registry containing only the tools
// that the profile permits. Semantics:
//
//   - Profile == nil  → returns reg unchanged (no profile = no narrowing).
//   - Tools.Deny      → always wins; matching tools are removed.
//   - Tools.Allow     → if non-empty, only matching tools survive.
//   - Both lists are glob patterns; trailing "*" matches any suffix and
//     a bare "*" matches everything.
//
// The original registry is not mutated.
func FilterRegistry(reg *tools.Registry, p *Profile) *tools.Registry {
	if reg == nil {
		return nil
	}
	if p == nil {
		return reg
	}
	allow := p.Tools.Allow
	deny := p.Tools.Deny
	if len(allow) == 0 && len(deny) == 0 {
		return reg
	}

	out := tools.New()
	for _, t := range reg.List() {
		name := t.Name()
		if matchAny(deny, name) {
			continue
		}
		if len(allow) > 0 && !matchAny(allow, name) {
			continue
		}
		out.MustRegister(t)
	}
	return out
}

// matchAny returns true when name matches any of the glob patterns.
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}

// matchGlob is a deliberately small glob: full match, or a trailing "*"
// that matches any suffix. The same semantics as tools.Registry.Filter so
// that an operator who knows skill allowed_tools knows profile globs.
func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == name
}
