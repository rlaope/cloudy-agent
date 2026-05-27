package db

import (
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds every db.* tool whose backend has at least one configured
// client. If no clients were established, the group "db" is marked skipped
// with a reason composed from per-endpoint dial errors.
func RegisterAll(reg *tools.Registry, clients Clients, skipReasons []string) {
	if clients.Empty() {
		reason := "no database endpoints configured"
		if len(skipReasons) > 0 {
			reason = "no usable database endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("db", reason)
		return
	}

	if len(clients.Redis) > 0 {
		reg.MustRegister(
			newRedisInfoTool(clients.Redis),
			newRedisDBSizeTool(clients.Redis),
			newRedisScanTool(clients.Redis),
			newRedisInspectKeyTool(clients.Redis),
			newRedisSlowlogTool(clients.Redis),
			newRedisClientListTool(clients.Redis),
		)
	}
	if len(clients.Postgres) > 0 {
		reg.MustRegister(
			newPGVersionTool(clients.Postgres),
			newPGStatActivityTool(clients.Postgres),
			newPGStatDatabaseTool(clients.Postgres),
			newPGStatReplicationTool(clients.Postgres),
			newPGLocksTool(clients.Postgres),
			newPGTableSizeTool(clients.Postgres),
		)
	}
	if len(clients.MySQL) > 0 {
		reg.MustRegister(
			newMySQLVersionTool(clients.MySQL),
			newMySQLProcesslistTool(clients.MySQL),
			newMySQLGlobalStatusTool(clients.MySQL),
			newMySQLGlobalVariablesTool(clients.MySQL),
			newMySQLEngineStatusTool(clients.MySQL),
			newMySQLTopTableSizeTool(clients.MySQL),
		)
	}
}
