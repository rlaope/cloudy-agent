package gateway

import "github.com/rlaope/cloudy/internal/core/tools"

// RegisterAll adds the gateway.* local status tools. These tools only inspect
// cloudy's own config and process environment; they do not touch monitored
// infrastructure.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(newStatusTool())
}
