package wiring

import (
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/permission"
)

// RebuildOpts carries the small handful of inputs that vary between callers
// (kubeconfig path / explicit context name from a `--context` flag). Every
// other input is read from the supplied *config.Config so the three callers
// — cmd/main.go (boot), internal/setup/wizard.go (post-/setup save), and
// internal/tui/setupchat.go (in-TUI /setup) — cannot diverge.
type RebuildOpts struct {
	KubeconfigPath string
	ContextName    string
}

// Rebuild is the single owner of "build a tools.Registry from cfg, install
// it as the active registry". It loads the active permission profile
// internally, builds the registry with the full Options shape, calls
// Replace(), and returns the result along with any non-fatal warning
// (currently *KubeWarning when no kubeconfig is reachable).
//
// Why this exists: before extraction the same sequence was inlined at
// three call sites with subtly different inputs (cmd/main.go passed only
// three Options fields; the two setup callers passed all eight). A fourth
// callsite — a future profile-switch or file-watch reload — would have
// tempted a fourth divergent copy. Funnelling through Rebuild collapses
// the surface to one function that every reload must call.
//
// cfg is a value (not a pointer) because config.Config is the cloudy
// canonical configuration value-type — Load() and Default() both return
// it by value. The returned registry is installed as the package-global
// active registry via Replace(). Callers do not call Replace themselves.
func Rebuild(cfg config.Config, opts RebuildOpts) (*tools.Registry, error) {
	activeProfile, _ := permission.LoadActive()

	reg, warn := BuildRegistry(Options{
		KubeconfigPath: opts.KubeconfigPath,
		ContextName:    opts.ContextName,
		Contexts:       cfg.Contexts,
		Profile:        activeProfile,
		PromEndpoints:  cfg.Prometheus,
		Databases:      cfg.Databases,
		Logs:           cfg.Logs,
		Tracing:        cfg.Tracing,
		Pprof:          cfg.Pprof,
		NodeInspectors: cfg.NodeInspectors,
		Alertmanager:   cfg.Alertmanager,
		ArgoCD:         cfg.ArgoCD,
		PagerDuty:      cfg.PagerDuty,
		DockerHosts:    cfg.DockerHosts,
		CloudAWS:       cfg.CloudAWS,
		CloudGCP:       cfg.CloudGCP,
		CloudAzure:     cfg.CloudAzure,
	})
	Replace(reg)
	return reg, warn
}
