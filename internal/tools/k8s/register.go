package k8s

import "github.com/rlaope/cloudy/internal/tools"

// RegisterAll adds every k8s.* read-only tool to reg, all bound to the same
// Hub. Wiring layers call this once per process; tests construct their own
// Hub and call this directly.
func RegisterAll(reg *tools.Registry, hub *Hub) {
	reg.MustRegister(
		NewListPodsTool(hub),
		NewListNodesTool(hub),
		NewListNamespacesTool(hub),
		NewDescribePodTool(hub),
		NewEventsTool(hub),
		NewLogsTool(hub),
		NewTopPodsTool(hub),
		NewTopNodesTool(hub),
	)
}
