package synthetic

import "github.com/rlaope/cloudy/internal/core/tools"

// RegisterAll adds the synthetic.* probe tools. The group needs no backend
// configuration — it makes outbound GET/HEAD requests from the host cloudy
// runs on — so it is always registered, like the ebpf group's tools once the
// platform gate passes.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(newHTTPCheckTool())
}
