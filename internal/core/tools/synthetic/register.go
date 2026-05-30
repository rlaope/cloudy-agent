package synthetic

import "github.com/rlaope/cloudy/internal/core/tools"

// RegisterAll adds the synthetic.* probe tools. The group needs no backend
// configuration — it makes outbound GET/HEAD requests from the host cloudy
// runs on — so it is always registered (like change.recent: RiskLow, no
// config gate). The probe's SSRF posture is documented in docs/SAFETY.md and
// enforced by the dial-time link-local guard in http_check.go.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(newHTTPCheckTool())
}
