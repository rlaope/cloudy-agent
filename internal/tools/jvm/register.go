package jvm

import "github.com/rlaope/cloudy/internal/tools"

// RegisterAll adds every jvm.* local-exec tool to reg.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(
		NewJstatGCTool(),
		NewJcmdGCTool(),
		NewJcmdThreadTool(),
		NewAsyncProfileTool(),
	)
}
