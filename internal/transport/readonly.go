// Package transport provides the read-only enforcement layer.
//
// The contract: every outbound HTTP request issued by cloudy passes through
// ReadOnlyRoundTripper, which rejects any method outside the whitelist. This
// is the first of cloudy's three independent read-only guards (the others
// are the Kubernetes verb wrapper and the ClusterRole RBAC).
package transport

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrReadOnlyViolation is returned when an outbound HTTP request uses a
// method not allowed by the read-only contract.
var ErrReadOnlyViolation = errors.New("transport: read-only violation")

// AllowedMethods is the immutable set of HTTP methods cloudy may use.
// Any method outside this set must be rejected at the transport layer.
var AllowedMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodOptions: {},
}

// ReadOnlyRoundTripper wraps another http.RoundTripper and rejects any
// request whose method is not in AllowedMethods. The wrapped tripper is
// only invoked for permitted requests.
type ReadOnlyRoundTripper struct {
	Inner http.RoundTripper
}

// New returns a ReadOnlyRoundTripper wrapping inner. If inner is nil,
// http.DefaultTransport is used.
func New(inner http.RoundTripper) *ReadOnlyRoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &ReadOnlyRoundTripper{Inner: inner}
}

// RoundTrip implements http.RoundTripper.
func (r *ReadOnlyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("transport: nil request")
	}
	if _, ok := AllowedMethods[req.Method]; !ok {
		return nil, fmt.Errorf("%w: method %s on %s", ErrReadOnlyViolation, req.Method, req.URL)
	}
	return r.Inner.RoundTrip(req)
}

// Wrap returns a function suitable for use with k8s.io/client-go's
// rest.Config.WrapTransport: it wraps any http.RoundTripper in a
// ReadOnlyRoundTripper.
func Wrap(inner http.RoundTripper) http.RoundTripper {
	return New(inner)
}
