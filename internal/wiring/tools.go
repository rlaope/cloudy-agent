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

	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/alert"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/cloud"
	"github.com/rlaope/cloudy/internal/core/tools/correlate"
	"github.com/rlaope/cloudy/internal/core/tools/db"
	"github.com/rlaope/cloudy/internal/core/tools/dockerlog"
	"github.com/rlaope/cloudy/internal/core/tools/ebpf"
	"github.com/rlaope/cloudy/internal/core/tools/gitops"
	"github.com/rlaope/cloudy/internal/core/tools/gpu"
	"github.com/rlaope/cloudy/internal/core/tools/jvm"
	"github.com/rlaope/cloudy/internal/core/tools/k8s"
	tlog "github.com/rlaope/cloudy/internal/core/tools/log"
	"github.com/rlaope/cloudy/internal/core/tools/metric"
	"github.com/rlaope/cloudy/internal/core/tools/oncall"
	"github.com/rlaope/cloudy/internal/core/tools/perf"
	"github.com/rlaope/cloudy/internal/core/tools/prom"
	"github.com/rlaope/cloudy/internal/core/tools/py"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
	"github.com/rlaope/cloudy/internal/permission"
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
	// Alertmanager is the list of Alertmanager v2 endpoints.
	Alertmanager []config.AlertmanagerEndpoint
	// ArgoCD is the list of Argo CD API endpoints.
	ArgoCD []config.ArgoCDEndpoint
	// PagerDuty is the list of PagerDuty accounts for the oncall group.
	PagerDuty []config.PagerDutyEndpoint
	// DockerHosts is the list of Docker daemons cloudy may inspect.
	DockerHosts []config.DockerHost
	// CloudAWS / CloudGCP / CloudAzure are the cloud-provider accounts cloudy
	// may query read-only via the operator's aws/gcloud/az CLIs.
	CloudAWS   []config.AWSAccount
	CloudGCP   []config.GCPProject
	CloudAzure []config.AzureAccount
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

	alertClients, alertSkips := alert.BuildClients(opts.Alertmanager, opts.PromEndpoints)
	alert.RegisterAll(reg, alertClients, alertSkips)

	gitopsClients, gitopsSkips := gitops.BuildClients(opts.ArgoCD)
	gitops.RegisterAll(reg, gitopsClients, gitopsSkips)

	oncallClients, oncallSkips := oncall.BuildClients(opts.PagerDuty)
	oncall.RegisterAll(reg, oncallClients, oncallSkips)

	cloudClients, cloudSkips := cloud.BuildClients(opts.CloudAWS, opts.CloudGCP, opts.CloudAzure)
	cloud.RegisterAll(reg, cloudClients, cloudSkips)

	perfClients, perfSkips := perf.BuildClients(opts.Pprof, opts.NodeInspectors)
	perf.RegisterAll(reg, perfClients, perfSkips)

	ebpf.RegisterAll(reg)

	// change.* spans k8s, docker, and cloud control-plane audit logs. Register
	// when at least one backend is available; skip the whole group only when
	// none is. cloudAudit folds CloudTrail / Cloud Audit Logs / Activity Log
	// onto the change timeline; nil when no cloud provider is configured. Built
	// from the cloudClients already constructed above.
	dockerHub, dockerErr := buildDockerHub(opts.DockerHosts)
	cloudAudit := cloud.NewAuditChangeSource(cloudClients)
	if hub == nil && dockerHub == nil && cloudAudit == nil {
		reason := "no kubeconfig, docker hosts, or cloud provider configured"
		if dockerErr != nil {
			reason = fmt.Sprintf("no kubeconfig or cloud provider; docker hosts configured but unavailable: %v", dockerErr)
		}
		reg.MarkSkipped("change", reason)
	} else {
		change.RegisterAll(reg, hub, dockerHub, cloudAudit)
	}

	// metric.* is Docker-only: container-level resource sampling. The k8s
	// metric path already lives in prom.* and k8s.top_pods/top_nodes, so the
	// group is skipped (not an error) when no docker hosts are configured.
	// Reuses the dockerHub already built for the change group above.
	if dockerHub == nil {
		reason := "no docker hosts configured (k8s metrics via prom/top_pods)"
		if dockerErr != nil {
			reason = fmt.Sprintf("docker hosts configured but unavailable: %v", dockerErr)
		}
		reg.MarkSkipped("metric", reason)
	} else {
		metric.RegisterAll(reg, dockerHub)
	}

	// log.container is the Docker-host side of log inquiry; it shares the
	// "log" namespace with the HTTP log group registered above. When a docker
	// hub is present we register it and clear any "log" skip that the HTTP
	// pass may have set with no Loki/ES configured — otherwise the group would
	// be simultaneously skipped and have a registered tool, which would make
	// the skill validator suppress references to a live tool. Reuses the
	// dockerHub already built for the change group above.
	if dockerHub != nil {
		dockerlog.RegisterAll(reg, dockerHub)
		reg.UnmarkSkipped("log")
	}

	// correlate.* joins the change timeline (k8s + docker + Argo CD sync) with
	// metric/log/trace symptom signals into one evidence chain. Register when
	// ANY signal source exists — change backends (k8s/docker/argo) or symptom
	// backends (prom/loki/jaeger), since symptom-only setups are valid; skip the
	// group only when none do. Reuses hub / dockerHub / Argo / prom / log /
	// trace clients already built above.
	// cloudTrace folds AWS X-Ray trace symptoms onto the correlate timeline;
	// nil when no AWS account is configured. Built from the cloudClients already
	// constructed above so an AWS-only setup still lights up correlate.
	cloudTrace := cloud.NewTraceSymptomSource(cloudClients)
	if hub == nil && dockerHub == nil && len(gitopsClients.Argo) == 0 &&
		len(promClients) == 0 && len(logClients.Loki) == 0 && len(traceClients.Jaeger) == 0 &&
		cloudTrace == nil {
		reg.MarkSkipped("correlate", "no kubeconfig, docker hosts, Argo CD, Prometheus, Loki, Jaeger, or AWS X-Ray endpoint configured")
	} else {
		correlate.RegisterAll(reg, hub, dockerHub, gitopsClients.Argo, promClients, logClients, traceClients, cloudTrace)
	}

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
// into a *k8sclient.Hub. Single-context mode is preserved when Contexts is empty.
func buildHub(opts Options) (*k8sclient.Hub, error) {
	contexts := opts.Contexts
	if len(contexts) == 0 && opts.ContextName != "" {
		contexts = []string{opts.ContextName}
	}
	return k8sclient.NewHub(opts.KubeconfigPath, contexts)
}

// buildDockerHub returns a *dockerclient.Hub for the configured Docker hosts,
// or (nil, nil) when none are configured. A non-nil error means hosts WERE
// configured but the hub could not be built, so callers can report an honest
// skip reason instead of "no docker hosts configured". Client connections are
// built lazily on first use.
func buildDockerHub(hosts []config.DockerHost) (*dockerclient.Hub, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	return dockerclient.NewHub(hosts)
}

// buildPromClients converts a slice of PrometheusEndpoint config entries into
// a map of named promclient.Client values, resolving credentials from the
// environment.
func buildPromClients(endpoints []config.PrometheusEndpoint) map[string]*promclient.Client {
	clients := make(map[string]*promclient.Client, len(endpoints))
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
		c, err := promclient.NewClient(ep.URL, ep.BasicUser, basicPass, bearer)
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
