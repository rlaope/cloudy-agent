package wiring

import (
	"context"
	"fmt"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/transport"

	// Side-effect imports: each tool package's init() registers its Detector
	// with discovery.Register at startup. Importing them here is the only
	// place we attach the detector graph for the wiring-level Run.
	_ "github.com/rlaope/cloudy/internal/core/tools/db"
	_ "github.com/rlaope/cloudy/internal/core/tools/log"
	_ "github.com/rlaope/cloudy/internal/core/tools/perf"
	_ "github.com/rlaope/cloudy/internal/core/tools/prom"
	_ "github.com/rlaope/cloudy/internal/core/tools/trace"
)

// DiscoveryOptions configures a single Run.
type DiscoveryOptions struct {
	KubeconfigPath string
	Contexts       []string

	// Profile narrows the Hub's namespace view; reused from BuildRegistry.
	Profile *permission.Profile

	// Hints carry user-supplied external HTTP and DB endpoints — these are
	// propagated into the Env so detectors emit External Findings for them.
	HTTPHints []config.HTTPEndpoint
	DBHints   []config.DatabaseEndpoint
}

// RunDiscovery builds an Env from opts, calls discovery.Run, and returns the
// merged Finding list. The caller (typically the /setup wizard) is
// responsible for filtering, converting Findings to config entries, and
// finally calling BuildRegistry + Replace.
//
// Returns a non-fatal note string when the Hub could not be built (no
// kubeconfig) so the caller can show it to the user; the returned Findings
// then come solely from Hints.
func RunDiscovery(ctx context.Context, opts DiscoveryOptions) ([]discovery.Finding, string, error) {
	hub, hubErr := k8sclient.NewHub(opts.KubeconfigPath, opts.Contexts)
	var note string
	if hubErr != nil {
		note = fmt.Sprintf("kubernetes hub unavailable: %v", hubErr)
		hub = nil
	} else if opts.Profile != nil {
		hub.WithNamespaceChecker(func(ns string) error {
			return permission.MatchNamespace(opts.Profile, ns)
		})
	}

	env := discovery.Env{
		Hub:     hub,
		Hints:   opts.HTTPHints,
		DBHints: opts.DBHints,
	}

	// Build a ServiceProxy if any restConfig is available — first context wins
	// for v0; multi-context detectors handle per-context routing internally.
	if hub != nil {
		if cli, err := hub.Get(""); err == nil {
			if cfg := cli.RESTConfig(); cfg != nil {
				if proxy, perr := transport.NewServiceProxy(cfg); perr == nil {
					env.Proxy = proxy
					env.HTTPClient = proxy.HTTPClient()
				}
			}
		}
	}

	return discovery.Run(ctx, env), note, nil
}
