package gateway

import (
	"bytes"
	"fmt"
)

// FormatText renders a compact operator-facing gateway status report.
func FormatText(rep Report) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "gateway ready=%v enabled=%v listen=%s\n", rep.Ready, rep.Enabled, rep.Listen)
	if rep.PublicURL != "" {
		fmt.Fprintf(&b, "public_url=%s\n", rep.PublicURL)
	}
	fmt.Fprintf(&b, "session_map=%s\n", rep.SessionPath)
	for _, warning := range rep.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", warning)
	}
	for _, platform := range rep.Platforms {
		fmt.Fprintf(&b, "\n%s enabled=%v ready=%v", platform.Platform, platform.Enabled, platform.Ready)
		if platform.Mode != "" {
			fmt.Fprintf(&b, " mode=%s", platform.Mode)
		}
		fmt.Fprintln(&b)
		for _, note := range platform.Notes {
			fmt.Fprintf(&b, "  note: %s\n", note)
		}
		for _, endpoint := range platform.Endpoints {
			fmt.Fprintf(&b, "  endpoint: %s\n", endpoint)
		}
		for _, req := range platform.Requirements {
			state := "ok"
			if req.Required && !req.Set {
				state = "missing"
			}
			required := "optional"
			if req.Required {
				required = "required"
			}
			fmt.Fprintf(&b, "  %s: %s (%s)", req.Key, state, required)
			if req.Detail != "" {
				fmt.Fprintf(&b, " - %s", req.Detail)
			}
			fmt.Fprintln(&b)
		}
	}
	return b.String()
}
