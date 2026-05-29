package change

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds the change.recent tool to reg, bound to whichever backends
// are available. A k8s source is added when k8sHub is non-nil; a docker source
// when dockerHub is non-nil; a cloud-audit source (cloudAudit) when non-nil.
// With no source available this is a no-op — the wiring layer marks the
// "change" group skipped instead.
//
// cloudAudit is built by the wiring layer (cloud.NewAuditChangeSource) and
// passed in as a plain ChangeSource so this package does not depend on the
// cloud package; nil means no cloud provider is configured.
func RegisterAll(reg *tools.Registry, k8sHub *k8sclient.Hub, dockerHub *dockerclient.Hub, cloudAudit ChangeSource) {
	var sources []ChangeSource
	if k8sHub != nil {
		sources = append(sources, NewK8sSource(k8sHub))
	}
	if dockerHub != nil {
		sources = append(sources, NewDockerSource(dockerHub))
	}
	if cloudAudit != nil {
		sources = append(sources, cloudAudit)
	}
	if len(sources) == 0 {
		return
	}
	reg.MustRegister(NewRecentTool(sources...))
}
