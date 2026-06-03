package discovery

import (
	"net/http"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/transport"
)

// Env carries everything a Detector needs to probe an environment. Detectors
// must treat Env as read-only and tolerate any field being nil (e.g. Hub is
// nil when no kubeconfig is configured — the detector should return nil in
// that case).
type Env struct {
	Hub   *k8sclient.Hub
	Proxy *transport.ServiceProxy
	// HTTPClient is the apiserver-authenticated, read-only client to use for
	// any probe against an EndpointURL produced via Proxy. When Proxy is nil,
	// HTTPClient may also be nil; detectors should fall back to a context-
	// local http.Client.
	HTTPClient *http.Client
	// Hints carries user-supplied external endpoints from /setup (or
	// config.yaml). Detectors can include these directly as External findings.
	Hints []config.HTTPEndpoint
	// DBHints carries user-supplied external DB endpoints.
	DBHints []config.DatabaseEndpoint
}
