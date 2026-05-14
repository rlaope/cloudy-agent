package transport

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

// ServiceProxy converts a Kubernetes (namespace, service, scheme, port) tuple
// into an apiserver services/proxy URL and provides an *http.Client that
// authenticates with the apiserver while keeping cloudy's read-only contract.
//
// Spec reference: https://kubernetes.io/docs/concepts/cluster-administration/proxies/
//
//	/api/v1/namespaces/<ns>/services/<scheme>:<svc>:<port>/proxy/<path>
//
// All requests issued through HTTPClient() go through ReadOnlyRoundTripper so
// the GET/HEAD/OPTIONS whitelist is preserved end-to-end.
type ServiceProxy struct {
	apiHost string // e.g. "https://10.0.0.1:6443"
	client  *http.Client
}

// NewServiceProxy builds a ServiceProxy from a *rest.Config (typically the
// loaded kubeconfig). The returned http.Client uses the apiserver's bearer
// token / client cert authentication, wrapped by ReadOnlyRoundTripper.
func NewServiceProxy(cfg *rest.Config) (*ServiceProxy, error) {
	if cfg == nil {
		return nil, fmt.Errorf("transport: rest.Config must not be nil")
	}

	// rest.TransportFor returns an http.RoundTripper that handles TLS, bearer
	// token, exec credential plugins, and any other auth configured in cfg.
	authRT, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("transport: build apiserver transport: %w", err)
	}

	// Wrap with ReadOnlyRoundTripper to enforce the GET/HEAD/OPTIONS contract.
	readOnlyRT := New(authRT)

	return &ServiceProxy{
		apiHost: strings.TrimRight(cfg.Host, "/"),
		client: &http.Client{
			Transport: readOnlyRT,
			Timeout:   30 * time.Second,
		},
	}, nil
}

// URL builds the services/proxy URL for (ns, svc, scheme, port) joined with
// path. scheme is "http" or "https"; port may be a name or a numeric string.
// path is appended after /proxy (leading slash optional).
//
// Each URL segment (ns, svc, scheme, port) is validated to contain no '/'
// characters before path-escaping, to prevent URL injection. An empty string
// is returned when any segment contains '/'.
//
// The services/proxy path format is:
//
//	/api/v1/namespaces/<ns>/services/<scheme>:<svc>:<port>/proxy/<path>
//
// When scheme is empty the triplet collapses to "<svc>:<port>" (no prefix).
func (p *ServiceProxy) URL(ns, svc, scheme, port, path string) string {
	// Reject segments containing '/' to prevent path injection. Each segment
	// is a discrete URL path component; a slash would escape the intended
	// position in the hierarchy.
	for _, seg := range []string{ns, svc, scheme, port} {
		if strings.Contains(seg, "/") {
			return ""
		}
	}

	// Build the <scheme>:<svc>:<port> or <svc>:<port> service specifier.
	var svcSpec string
	if scheme != "" {
		svcSpec = url.PathEscape(scheme) + ":" + url.PathEscape(svc) + ":" + url.PathEscape(port)
	} else {
		svcSpec = url.PathEscape(svc) + ":" + url.PathEscape(port)
	}

	base := p.apiHost +
		"/api/v1/namespaces/" + url.PathEscape(ns) +
		"/services/" + svcSpec +
		"/proxy"

	if path == "" {
		return base
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// HTTPClient returns the apiserver-authenticated, read-only *http.Client.
func (p *ServiceProxy) HTTPClient() *http.Client { return p.client }
