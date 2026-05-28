package correlate

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/gitops"
	tlog "github.com/rlaope/cloudy/internal/core/tools/log"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// RegisterAll adds the correlate.workload tool to reg. The change sources are
// fixed at registration: a k8s change source when k8sHub is non-nil, a docker
// change source when dockerHub is non-nil, and an Argo CD sync source when at
// least one Argo client is wired. The symptom backends (prom/logs/traces) are
// threaded through so the tool can build metric/log/trace symptom sources from
// per-call args at Run time. With no change source AND no symptom backend this
// is a no-op — the wiring layer marks the "correlate" group skipped instead.
func RegisterAll(reg *tools.Registry, k8sHub *k8sclient.Hub, dockerHub *dockerclient.Hub, argo map[string]*gitops.ArgoClient, prom map[string]*promclient.Client, logs tlog.Clients, traces trace.Clients) {
	var sources []change.ChangeSource
	if k8sHub != nil {
		sources = append(sources, change.NewK8sSource(k8sHub))
	}
	if dockerHub != nil {
		sources = append(sources, change.NewDockerSource(dockerHub))
	}
	if src := newArgoSource(argo); src != nil {
		sources = append(sources, src)
	}
	// Register when any change source OR any symptom backend exists; a
	// symptom-only setup (e.g. prom + loki, no k8s/docker/argo) is still valid.
	hasSymptom := len(prom) > 0 || len(logs.Loki) > 0 || len(traces.Jaeger) > 0
	if len(sources) == 0 && !hasSymptom {
		return
	}
	reg.MustRegister(NewWorkloadTool(sources, prom, logs, traces, dockerHub))
}
