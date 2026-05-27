package tui

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// renderInventory formats the registry's tool-group inventory for inclusion
// in the TUI stream. Skipped groups show the reason; registered groups list
// their tool names.
func renderInventory(reg *tools.Registry) string {
	if reg == nil {
		return "[tools: registry unavailable]\n"
	}
	inv := reg.Inventory()
	if len(inv.Groups) == 0 {
		return "[tools: no groups]\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s  %-7s  %s\n", "GROUP", "STATUS", "DETAIL")
	for _, g := range inv.Groups {
		if g.Skipped {
			fmt.Fprintf(&b, "%-10s  %-7s  %s\n", g.Name, "skipped", g.Reason)
			continue
		}
		fmt.Fprintf(&b, "%-10s  %-7s  %s\n", g.Name, "ok", strings.Join(g.Tools, ", "))
	}
	return b.String()
}
