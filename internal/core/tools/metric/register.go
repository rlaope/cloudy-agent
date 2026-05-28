package metric

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds the metric.container_stats tool to reg when a Docker hub is
// available. With no docker hosts (dockerHub == nil) this is a no-op — the
// wiring layer marks the "metric" group skipped instead.
func RegisterAll(reg *tools.Registry, dockerHub *dockerclient.Hub) {
	if dockerHub == nil {
		return
	}
	reg.MustRegister(NewContainerStatsTool(dockerHub))
}
