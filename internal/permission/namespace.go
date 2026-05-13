package permission

import "errors"

// ErrNamespaceDenied is returned when a namespace matches an entry in
// Profile.Namespaces.Deny. Deny always wins over Allow.
var ErrNamespaceDenied = errors.New("permission: namespace denied by profile")

// ErrNamespaceNotAllowed is returned when Profile.Namespaces.Allow is
// non-empty and the namespace matches no entry in it.
var ErrNamespaceNotAllowed = errors.New("permission: namespace not in profile allow list")

// MatchNamespace reports whether the active profile permits operating in ns.
// Returns:
//   - nil when p is nil (no profile = no narrowing).
//   - nil when ns is empty (the empty-namespace case currently means "all
//     namespaces" and is left permissive; if narrowing all-namespace calls
//     becomes a requirement, tighten this branch).
//   - ErrNamespaceDenied when ns matches any glob in Namespaces.Deny.
//   - ErrNamespaceNotAllowed when Namespaces.Allow is non-empty and ns
//     matches no glob in Namespaces.Allow.
//
// Glob semantics match the Tools filter: full match, or trailing "*" matches
// any suffix; a bare "*" matches everything.
func MatchNamespace(p *Profile, ns string) error {
	if p == nil {
		return nil
	}
	if ns == "" {
		return nil
	}
	if matchAny(p.Namespaces.Deny, ns) {
		return ErrNamespaceDenied
	}
	if len(p.Namespaces.Allow) > 0 && !matchAny(p.Namespaces.Allow, ns) {
		return ErrNamespaceNotAllowed
	}
	return nil
}
