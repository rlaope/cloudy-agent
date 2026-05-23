package transport

import "errors"

// AllowedKubeVerbs is the immutable set of Kubernetes API verbs cloudy may use.
// The actual enforcement lives in two places: the HTTP-method whitelist in
// ReadOnlyRoundTripper (which all K8s client traffic flows through via
// rest.Config.WrapTransport in internal/tools/k8s/client.go) and the
// ClusterRole RBAC in manifests/rbac/. This constant exists so the
// manifest, docs, and any future RBAC-shaped code share a single source of
// truth for "what cloudy is allowed to do".
var AllowedKubeVerbs = map[string]struct{}{
	"get":   {},
	"list":  {},
	"watch": {},
}

// ErrKubeVerbViolation is returned when a Kubernetes operation requests a verb
// outside AllowedKubeVerbs. Kept exported because production code may match
// against it via errors.Is when surfacing a mutation-attempt diagnostic.
var ErrKubeVerbViolation = errors.New("transport: kube verb not allowed")
