package correlate

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/gitops"
)

// RegisterAll adds the correlate.workload tool to reg, bound to whichever
// signal sources are available: a k8s change source when k8sHub is non-nil, a
// docker change source when dockerHub is non-nil, and an Argo CD sync source
// when at least one Argo client is wired. With no source available this is a
// no-op — the wiring layer marks the "correlate" group skipped instead.
func RegisterAll(reg *tools.Registry, k8sHub *k8sclient.Hub, dockerHub *dockerclient.Hub, argo map[string]*gitops.ArgoClient) {
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
	if len(sources) == 0 {
		return
	}
	reg.MustRegister(newCorrelateTool(sources...))
}
