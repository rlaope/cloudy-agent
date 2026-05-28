package change

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds the change.recent tool to reg, bound to whichever backends
// are available. A k8s source is added when k8sHub is non-nil; a docker source
// when dockerHub is non-nil. With no source available this is a no-op — the
// wiring layer marks the "change" group skipped instead.
func RegisterAll(reg *tools.Registry, k8sHub *k8sclient.Hub, dockerHub *dockerclient.Hub) {
	var sources []ChangeSource
	if k8sHub != nil {
		sources = append(sources, NewK8sSource(k8sHub))
	}
	if dockerHub != nil {
		sources = append(sources, NewDockerSource(dockerHub))
	}
	if len(sources) == 0 {
		return
	}
	reg.MustRegister(NewRecentTool(sources...))
}
