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
// change source when dockerHub is non-nil, an Argo CD sync source when at least
// one Argo client is wired, a cloud control-plane audit source (cloudAudit)
// when non-nil, and a cloud-trace symptom source (cloudTrace) when non-nil. The
// symptom backends (prom/logs/traces) are threaded through so the tool can build
// metric/log/trace symptom sources from per-call args at Run time. With no
// change source AND no symptom backend this is a no-op — the wiring layer marks
// the "correlate" group skipped instead.
//
// cloudAudit and cloudTrace are built by the wiring layer
// (cloud.NewAuditChangeSource / cloud.NewTraceSymptomSource) and passed in as
// plain change.ChangeSource values so this package does not depend on the cloud
// package. cloudAudit emits cloud_audit change events (ranked candidate causes);
// cloudTrace emits trace symptom events. nil means the matching cloud signal is
// not configured.
func RegisterAll(reg *tools.Registry, k8sHub *k8sclient.Hub, dockerHub *dockerclient.Hub, argo map[string]*gitops.ArgoClient, prom map[string]*promclient.Client, logs tlog.Clients, traces trace.Clients, cloudAudit, cloudTrace change.ChangeSource) {
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
	if cloudAudit != nil {
		sources = append(sources, cloudAudit)
	}
	if cloudTrace != nil {
		sources = append(sources, cloudTrace)
	}
	// Register when any change source OR any symptom backend exists; a
	// symptom-only setup (e.g. prom + loki, no k8s/docker/argo) is still valid.
	hasSymptom := len(prom) > 0 || len(logs.Loki) > 0 || len(logs.ES) > 0 || len(traces.Jaeger) > 0 || len(traces.Tempo) > 0
	if len(sources) == 0 && !hasSymptom {
		return
	}
	reg.MustRegister(NewWorkloadTool(sources, prom, logs, traces, dockerHub))
}
