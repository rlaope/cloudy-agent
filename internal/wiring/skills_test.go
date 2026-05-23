package wiring_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/wiring"
)

// canonicalToolNames mirrors every tool name shipped under internal/tools/.
// Hand-maintained because building a real tools.Registry here would require
// live k8s.Hub / prom.Client etc. that the regression doesn't need; the
// trade-off is that adding a tool means appending one line to this slice.
var canonicalToolNames = []string{
	"k8s.list_pods", "k8s.list_nodes", "k8s.list_namespaces", "k8s.describe_pod",
	"k8s.events", "k8s.logs", "k8s.top_pods", "k8s.top_nodes",

	"prom.query", "prom.query_range", "prom.label_values", "prom.series",

	"log.loki_query_range", "log.loki_labels", "log.loki_label_values",
	"log.loki_series", "log.es_search", "log.es_indices", "log.es_cluster_health",

	"trace.tempo_get_trace", "trace.tempo_search",
	"trace.jaeger_services", "trace.jaeger_operations", "trace.jaeger_search_traces",

	"db.pg_version", "db.pg_stat_activity", "db.pg_stat_database",
	"db.pg_stat_replication", "db.pg_locks", "db.pg_top_table_size",
	"db.mysql_version", "db.mysql_processlist", "db.mysql_global_status",
	"db.mysql_global_variables", "db.mysql_engine_innodb_status", "db.mysql_top_table_size",
	"db.redis_info", "db.redis_dbsize", "db.redis_scan", "db.redis_inspect_key",
	"db.redis_slowlog", "db.redis_client_list",

	"perf.rbspy_dump", "perf.go_pprof_cpu", "perf.linux_perf_record",
	"perf.v8_inspector_cpu_profile",

	"jvm.async_profile", "jvm.jcmd_gc", "jvm.jcmd_thread_dump", "jvm.jstat_gc",

	"py.spy_dump", "py.spy_top_snapshot",

	"gpu.nvidia_smi", "gpu.dcgm_metrics",

	"ebpf.biolatency", "ebpf.tcptop", "ebpf.tcprtt", "ebpf.execsnoop",
	"ebpf.bpftrace_oneliner",
}

// stubTool mirrors the helper in internal/tools/registry_test.go; copied here
// rather than exported so the production package stays free of test scaffolding.
type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "stub: " + s.name }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Run(_ context.Context, _ json.RawMessage) (tools.Observation, error) {
	return tools.Observation{Text: "ok"}, nil
}

// TestSkillToolRefsAreValid pins every built-in skill's AllowedTools to a
// real tool name. The regression it guards against: k8s-incident shipped
// referencing k8s.get_pod (typo for k8s.describe_pod) and the skill silently
// failed only at first invocation.
func TestSkillToolRefsAreValid(t *testing.T) {
	t.Parallel()

	builtin, err := skills.LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin: %v", err)
	}
	reg := skills.New(builtin)

	tr := tools.New()
	for _, name := range canonicalToolNames {
		tr.MustRegister(stubTool{name: name})
	}

	if err := wiring.ValidateSkillToolRefs(reg, tr); err != nil {
		t.Fatalf("built-in skills reference unknown tools: %v", err)
	}
}

// TestSkillToolRefs_CatchesUnknown is the inverse: ensures a future no-op
// regression in the validator does not leave TestSkillToolRefsAreValid
// passing on broken refs.
func TestSkillToolRefs_CatchesUnknown(t *testing.T) {
	t.Parallel()

	reg := skills.New([]*skills.Skill{{
		Name:         "fake",
		Description:  "fake skill",
		AllowedTools: []string{"k8s.list_pods", "k8s.does_not_exist"},
		SystemPrompt: "irrelevant",
	}})

	tr := tools.New()
	tr.MustRegister(stubTool{name: "k8s.list_pods"})

	err := wiring.ValidateSkillToolRefs(reg, tr)
	if err == nil {
		t.Fatal("expected error for unknown tool ref, got nil")
	}
	if !strings.Contains(err.Error(), "k8s.does_not_exist") {
		t.Errorf("error should name the unknown tool; got: %v", err)
	}
}
