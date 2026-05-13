// Package wiring assembles the tool registry, skill registry, and LLM provider
// from config and environment, keeping those decisions out of both the TUI and
// the CLI sub-commands.
package wiring

import (
	"fmt"
	"os"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/gpu"
	"github.com/rlaope/cloudy/internal/tools/jvm"
	"github.com/rlaope/cloudy/internal/tools/k8s"
	"github.com/rlaope/cloudy/internal/tools/prom"
	"github.com/rlaope/cloudy/internal/tools/py"
)

// Options controls which tool groups are built.
type Options struct {
	// KubeconfigPath is the path to a kubeconfig file. Empty = default discovery.
	KubeconfigPath string
	// ContextName is the kubeconfig context to use. Empty = current context.
	// Ignored when Contexts is non-empty (multi-context mode).
	ContextName string
	// Contexts is the explicit list of kubeconfig contexts the Hub should
	// expose. Empty = single-context mode using the kubeconfig current-context.
	Contexts []string
	// Profile, when non-nil, narrows the registry: the namespace checker is
	// installed on the Hub and permission.FilterRegistry is applied before
	// the registry is returned.
	Profile *permission.Profile
	// PromEndpoints is the list of Prometheus endpoints from config.
	PromEndpoints []config.PrometheusEndpoint
	// EnableJVM registers jvm.* tools (default: true; always registered).
	EnableJVM bool
	// EnablePython registers py.* tools (default: true; always registered).
	EnablePython bool
	// EnableGPU registers gpu.* tools (default: true; always registered).
	EnableGPU bool
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
//
// When opts.Profile is non-nil, the namespace allow/deny check is wired into
// the K8s Hub and permission.FilterRegistry narrows the final tool set.
func BuildRegistry(opts Options) (*tools.Registry, error) {
	reg := tools.New()
	var kubeWarn error

	// --- Kubernetes tools ---
	// Multi-context mode short-circuits ContextName; the Hub manages clients.
	contexts := opts.Contexts
	if len(contexts) == 0 && opts.ContextName != "" {
		contexts = []string{opts.ContextName}
	}
	hub, err := k8s.NewHub(opts.KubeconfigPath, contexts)
	if err != nil {
		kubeWarn = &KubeWarning{Err: err}
	} else {
		if opts.Profile != nil {
			profile := opts.Profile
			hub.WithNamespaceChecker(func(ns string) error {
				return permission.MatchNamespace(profile, ns)
			})
		}
		reg.MustRegister(
			k8s.NewListPodsTool(hub),
			k8s.NewListNodesTool(hub),
			k8s.NewListNamespacesTool(hub),
			k8s.NewDescribePodTool(hub),
			k8s.NewEventsTool(hub),
			k8s.NewLogsTool(hub),
			k8s.NewTopPodsTool(hub),
			k8s.NewTopNodesTool(hub),
		)
	}

	// --- Prometheus tools ---
	promClients := buildPromClients(opts.PromEndpoints)
	if len(promClients) > 0 {
		reg.MustRegister(
			prom.NewQueryTool(promClients),
			prom.NewQueryRangeTool(promClients),
			prom.NewLabelValuesTool(promClients),
			prom.NewSeriesTool(promClients),
		)
		// DCGM needs prom clients too.
		reg.MustRegister(gpu.NewDCGMTool(promClients))
	}

	// --- JVM tools (local exec; always registered) ---
	if opts.EnableJVM {
		reg.MustRegister(
			jvm.NewJstatGCTool(),
			jvm.NewJcmdGCTool(),
			jvm.NewJcmdThreadTool(),
			jvm.NewAsyncProfileTool(),
		)
	}

	// --- Python tools (local exec; always registered) ---
	if opts.EnablePython {
		reg.MustRegister(
			py.NewSpyDumpTool(),
			py.NewSpyTopTool(),
		)
	}

	// --- GPU nvidia-smi (local exec; always registered) ---
	if opts.EnableGPU {
		reg.MustRegister(gpu.NewNvidiaSMITool())
	}

	// Apply Permission Profile tool-name allow/deny last so wiring owns the
	// full narrowing pipeline; callers no longer need to call FilterRegistry
	// themselves.
	if opts.Profile != nil {
		reg = permission.FilterRegistry(reg, opts.Profile)
	}

	return reg, kubeWarn
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
			// Skip misconfigured endpoints; wiring continues.
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
