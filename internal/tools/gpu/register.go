package gpu

import (
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/prom"
)

// RegisterAll adds gpu.* tools. nvidia-smi is local-exec and always
// registered; DCGM is only added when at least one prometheus client is
// available, since it queries Prom for DCGM metrics.
func RegisterAll(reg *tools.Registry, promClients map[string]*prom.Client) {
	reg.MustRegister(NewNvidiaSMITool())
	if len(promClients) > 0 {
		reg.MustRegister(NewDCGMTool(promClients))
	}
}
