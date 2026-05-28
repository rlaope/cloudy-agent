package dockerlog

import (
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds the log.container tool to reg when a Docker hub is
// available. With no docker hosts (dockerHub == nil) this is a no-op — the
// wiring layer decides the "log" group's skip state, taking both the HTTP log
// backends and the Docker hub into account.
func RegisterAll(reg *tools.Registry, dockerHub *dockerclient.Hub) {
	if dockerHub == nil {
		return
	}
	reg.MustRegister(NewContainerLogsTool(dockerHub))
}
