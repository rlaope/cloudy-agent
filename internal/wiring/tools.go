// Package wiring assembles the tool registry, skill registry, and LLM provider
// from config and environment, keeping those decisions out of both the TUI and
// the CLI sub-commands.
//
// BuildRegistry's responsibility is intentionally narrow: build dependency
// containers (Hub, Prom client map) and call each tool group's self-contained
// RegisterAll helper. Adding a new tool group is "import its package, call
// RegisterAll" — the function does not learn what tools exist inside.
package wiring

import (
	"context"
	"fmt"
	"os"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/db"
	"github.com/rlaope/cloudy/internal/tools/ebpf"
	"github.com/rlaope/cloudy/internal/tools/gpu"
	"github.com/rlaope/cloudy/internal/tools/jvm"
	"github.com/rlaope/cloudy/internal/tools/k8s"
	tlog "github.com/rlaope/cloudy/internal/tools/log"
	"github.com/rlaope/cloudy/internal/tools/perf"
	"github.com/rlaope/cloudy/internal/tools/prom"
	"github.com/rlaope/cloudy/internal/tools/py"
	"github.com/rlaope/cloudy/internal/tools/trace"
)

// Options controls dependency construction. The set is intentionally small —
// callers configure *what infrastructure exists*, not which tools to enable.
// Tool groups that need no external deps (jvm/py/gpu local exec) are always
// registered.
type Options struct {
	// KubeconfigPath is the path to a kubeconfig file. Empty = default discovery.
	KubeconfigPath string
	// ContextName is the kubeconfig context to use. Empty = current context.
	// Ignored when Contexts is non-empty (multi-context mode).
	ContextName string
	// Contexts is the explicit list of kubeconfig contexts the Hub should
	// expose. Empty = single-context mode using the kubeconfig current-context.
	Contexts []string
	// Profile, when non-nil, narrows the registry through permission.Apply:
	// namespace checker installed on the Hub, tool allow/deny applied last.
	Profile *permission.Profile
	// PromEndpoints is the list of Prometheus endpoints from config.
	PromEndpoints []config.PrometheusEndpoint
	// Databases is the list of read-only database endpoints from config.
	Databases []config.DatabaseEndpoint
	// Logs is the list of log-search endpoints (loki / elasticsearch).
	Logs []config.HTTPEndpoint
	// Tracing is the list of tracing endpoints (tempo / jaeger).
	Tracing []config.HTTPEndpoint
	// Pprof is the list of Go services exposing /debug/pprof/*.
	Pprof []config.HTTPEndpoint
	// NodeInspectors is the list of Node V8 Inspector endpoints.
	NodeInspectors []config.HTTPEndpoint
}

// KubeWarning is a non-fatal warning returned by BuildRegistry when the
// Kubernetes client could not be constructed. The registry is still usable
// for prom/jvm/py/gpu tools.
type KubeWarning struct {
	Err error
}

func (w *KubeWarning) Error() string {
	return fmt.Sprintf("wiring: kubernetes client unavailable (%v) — k8s tools disabled", w.Err)
}

// BuildRegistry constructs a *tools.Registry from opts.
//
// K8s tool construction failures return the registry with a *KubeWarning
// rather than a hard error, so the CLI remains usable for help/version/skills
// even when no kubeconfig is present.
func BuildRegistry(opts Options) (*tools.Registry, error) {
	reg := tools.New()
	var kubeWarn error

	hub, err := buildHub(opts)
	if err != nil {
		kubeWarn = &KubeWarning{Err: err}
		reg.MarkSkipped("k8s", err.Error())
	} else {
		k8s.RegisterAll(reg, hub)
	}

	promClients := buildPromClients(opts.PromEndpoints)
	prom.RegisterAll(reg, promClients)
	gpu.RegisterAll(reg, promClients)
	jvm.RegisterAll(reg)
	py.RegisterAll(reg)

	dbClients, dbSkips := db.BuildClients(context.Background(), hub, opts.Databases)
	db.RegisterAll(reg, dbClients, dbSkips)

	logClients, logSkips := tlog.BuildClients(opts.Logs)
	tlog.RegisterAll(reg, logClients, logSkips)

	traceClients, traceSkips := trace.BuildClients(opts.Tracing)
	trace.RegisterAll(reg, traceClients, traceSkips)

	perfClients, perfSkips := perf.BuildClients(opts.Pprof, opts.NodeInspectors)
	perf.RegisterAll(reg, perfClients, perfSkips)

	ebpf.RegisterAll(reg)

	// Single Profile application point: namespace checker on the Hub plus
	// tool allow/deny filter on the returned registry.
	reg = permission.Apply(reg, opts.Profile, func(check func(string) error) {
		if hub != nil {
			hub.WithNamespaceChecker(check)
		}
	})
	return reg, kubeWarn
}

// buildHub resolves opts.Contexts / opts.ContextName / opts.KubeconfigPath
// into a *k8s.Hub. Single-context mode is preserved when Contexts is empty.
func buildHub(opts Options) (*k8s.Hub, error) {
	contexts := opts.Contexts
	if len(contexts) == 0 && opts.ContextName != "" {
		contexts = []string{opts.ContextName}
	}
	return k8s.NewHub(opts.KubeconfigPath, contexts)
}

// buildPromClients converts a slice of PrometheusEndpoint config entries into
// a map of named prom.Client values, resolving credentials from the environment.
func buildPromClients(endpoints []config.PrometheusEndpoint) map[string]*prom.Client {
	clients := make(map[string]*prom.Client, len(endpoints))
	for _, ep := range endpoints {
		if ep.URL == "" {
			continue
		}
		bearer := ""
		if ep.BearerEnv != "" {
			bearer = os.Getenv(ep.BearerEnv)
		}
		basicPass := ""
		if ep.BasicPassEnv != "" {
			basicPass = os.Getenv(ep.BasicPassEnv)
		}
		c, err := prom.NewClient(ep.URL, ep.BasicUser, basicPass, bearer)
		if err != nil {
			continue
		}
		key := ep.Name
		if key == "" {
			key = ep.URL
		}
		clients[key] = c
	}
	return clients
}
