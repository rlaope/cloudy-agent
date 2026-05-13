package permission

import "github.com/rlaope/cloudy/internal/tools"

// SetNamespaceChecker is the callback signature wiring layers expose for any
// component (e.g. k8s.Hub) that wants per-namespace narrowing installed by
// the active Profile. The closure passed in is the actual permission check.
type SetNamespaceChecker func(check func(ns string) error)

// Apply is the single entry point for narrowing a tool registry by an active
// Permission Profile. It does two things:
//
//  1. If installNS is non-nil, it registers a namespace allow/deny check
//     derived from p.Namespaces.{Allow,Deny}.
//  2. It returns a Tool registry filtered by p.Tools.{Allow,Deny}.
//
// p == nil is a no-op (returns reg unchanged, no checker installed). This
// keeps every caller's narrowing logic in this one function.
func Apply(reg *tools.Registry, p *Profile, installNS SetNamespaceChecker) *tools.Registry {
	if p == nil {
		return reg
	}
	if installNS != nil {
		profile := p
		installNS(func(ns string) error {
			return MatchNamespace(profile, ns)
		})
	}
	return FilterRegistry(reg, p)
}
