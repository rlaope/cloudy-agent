package prom

import "github.com/rlaope/cloudy/internal/tools"

// RegisterAll adds every prom.* tool to reg, sharing the same client map.
// Empty clients is a no-op so wiring can call this unconditionally.
func RegisterAll(reg *tools.Registry, clients map[string]*Client) {
	if len(clients) == 0 {
		return
	}
	reg.MustRegister(
		NewQueryTool(clients),
		NewQueryRangeTool(clients),
		NewLabelValuesTool(clients),
		NewSeriesTool(clients),
	)
}
