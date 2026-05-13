package transport

import (
	"errors"
	"strings"
)

// AllowedKubeVerbs is the immutable set of Kubernetes API verbs cloudy may use.
// This is enforced both at the cloudy client wrapper layer and via RBAC.
var AllowedKubeVerbs = map[string]struct{}{
	"get":   {},
	"list":  {},
	"watch": {},
}

// ErrKubeVerbViolation is returned when a Kubernetes operation requests a verb
// outside AllowedKubeVerbs.
var ErrKubeVerbViolation = errors.New("transport: kube verb not allowed")

// CheckVerb returns ErrKubeVerbViolation when verb (case-insensitive) is not
// in AllowedKubeVerbs. It is the helper every Kubernetes-facing tool MUST call
// before issuing a request.
func CheckVerb(verb string) error {
	if _, ok := AllowedKubeVerbs[strings.ToLower(verb)]; !ok {
		return ErrKubeVerbViolation
	}
	return nil
}
