package py

import "github.com/rlaope/cloudy/internal/core/tools"

// RegisterAll adds every py.* local-exec tool to reg.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(
		NewSpyDumpTool(),
		NewSpyTopTool(),
	)
}
