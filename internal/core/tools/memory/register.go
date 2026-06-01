package memory

import "github.com/rlaope/cloudy/internal/core/tools"

// RegisterAll adds the memory.* tool group. Like synthetic.*, it needs no
// backend configuration — it writes only to cloudy's local memory file — so it
// is always registered.
func RegisterAll(reg *tools.Registry) {
	reg.MustRegister(newRecordTool())
}
