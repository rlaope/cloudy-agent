package prom

import (
	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds every prom.* tool to reg, sharing the same client map.
// Empty clients records "no prometheus endpoints configured" in the Inventory
// and registers nothing.
func RegisterAll(reg *tools.Registry, clients map[string]*promclient.Client) {
	if len(clients) == 0 {
		reg.MarkSkipped("prom", "no prometheus endpoints configured")
		return
	}
	reg.MustRegister(
		NewQueryTool(clients),
		NewQueryRangeTool(clients),
		NewLabelValuesTool(clients),
		NewSeriesTool(clients),
		NewAnomalyTool(clients),
	)
}
