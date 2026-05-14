// Package transport provides the read-only enforcement layer.
//
// The contract: every outbound HTTP request issued by cloudy passes through
// ReadOnlyRoundTripper, which rejects any method outside the whitelist. This
// is the first of cloudy's three independent read-only guards (the others
// are the Kubernetes verb wrapper and the ClusterRole RBAC).
package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
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
		return nil, fmt.Errorf("%w: method %s on %s — %s",
			ErrReadOnlyViolation, req.Method, req.URL, readOnlyAlternative(req.Method))
	}
	return r.Inner.RoundTrip(req)
}

// readOnlyAlternative returns a one-line hint pointing the caller (and the
// LLM, when this error surfaces through a tool result) at a non-mutating verb
// that can satisfy the same intent. The text is intentionally generic — the
// transport layer does not know which backend was being targeted.
func readOnlyAlternative(method string) string {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return "cloudy is read-only by design; use a GET-based inspect/list/get tool instead of writing"
	case http.MethodDelete:
		return "cloudy cannot delete resources; use a list/get tool to inspect what you would have removed"
	default:
		return "cloudy permits only GET/HEAD/OPTIONS; switch to a read-only verb"
	}
}

// Wrap returns a function suitable for use with k8s.io/client-go's
// rest.Config.WrapTransport: it wraps any http.RoundTripper in a
// ReadOnlyRoundTripper.
func Wrap(inner http.RoundTripper) http.RoundTripper {
	return New(inner)
}

// DialContext exposes the underlying http.Transport's DialContext so callers
// that need a raw connection (e.g. a websocket dialer) can plug into the
// same network stack the read-only RoundTripper uses, without reaching into
// the unexported field with a type assertion.
//
// Falls back to net.Dialer.DialContext if Inner is not an *http.Transport
// (e.g. a chained custom tripper). That keeps the helper safe regardless of
// what the caller passed to New.
func (r *ReadOnlyRoundTripper) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if t, ok := r.Inner.(*http.Transport); ok && t.DialContext != nil {
		return t.DialContext(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}
