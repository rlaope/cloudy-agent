// Package wiring assembles the tool registry, skill registry, and LLM provider
// from config and environment, keeping those decisions out of both the TUI and
// the CLI sub-commands.
package wiring

import (
	"fmt"
	"os"

	"github.com/rlaope/cloudy/internal/config"
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
	ContextName string
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
func BuildRegistry(opts Options) (*tools.Registry, error) {
	reg := tools.New()
	var kubeWarn error

	// --- Kubernetes tools ---
	kClient, err := k8s.NewClient(opts.KubeconfigPath, opts.ContextName)
	if err != nil {
		kubeWarn = &KubeWarning{Err: err}
	} else {
		reg.MustRegister(
			k8s.NewListPodsTool(kClient),
			k8s.NewListNodesTool(kClient),
			k8s.NewListNamespacesTool(kClient),
			k8s.NewDescribePodTool(kClient),
			k8s.NewEventsTool(kClient),
			k8s.NewLogsTool(kClient),
			k8s.NewTopPodsTool(kClient),
			k8s.NewTopNodesTool(kClient),
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
