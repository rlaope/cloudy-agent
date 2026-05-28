package wiring_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/wiring"
)

// canonicalToolNames mirrors every tool name shipped under internal/tools/.
// Hand-maintained because building a real tools.Registry here would require
// live k8s.Hub / prom.Client etc. that the regression doesn't need; the
// trade-off is that adding a tool means appending one line to this slice.
var canonicalToolNames = []string{
	"k8s.list_pods", "k8s.list_nodes", "k8s.list_namespaces", "k8s.describe_pod",
	"k8s.events", "k8s.logs", "k8s.top_pods", "k8s.top_nodes",
	"k8s.list_deployments", "k8s.list_statefulsets", "k8s.list_daemonsets",
	"k8s.list_jobs", "k8s.list_cronjobs", "k8s.list_services",
	"k8s.list_ingresses", "k8s.list_hpa", "k8s.list_pdbs",
	"k8s.list_networkpolicies", "k8s.list_crds", "k8s.list_cr",

	"prom.query", "prom.query_range", "prom.label_values", "prom.series",

	"log.loki_query_range", "log.loki_labels", "log.loki_label_values",
	"log.loki_series", "log.es_search", "log.es_indices", "log.es_cluster_health",

	"trace.tempo_get_trace", "trace.tempo_search",
	"trace.service_graph", "trace.route_red",
	"trace.jaeger_services", "trace.jaeger_operations", "trace.jaeger_search_traces",

	"alert.list_active", "alert.list_silences", "alert.list_rules",

	"gitops.argo_list_apps", "gitops.argo_app_status", "gitops.argo_app_history",

	"db.pg_version", "db.pg_stat_activity", "db.pg_stat_database",
	"db.pg_stat_replication", "db.pg_locks", "db.pg_top_table_size",
	"db.mysql_version", "db.mysql_processlist", "db.mysql_global_status",
	"db.mysql_global_variables", "db.mysql_engine_innodb_status", "db.mysql_top_table_size",
	"db.redis_info", "db.redis_dbsize", "db.redis_scan", "db.redis_inspect_key",
	"db.redis_slowlog", "db.redis_client_list",

	"perf.rbspy_dump", "perf.go_pprof_cpu", "perf.linux_perf_record",
	"perf.v8_inspector_targets", "perf.v8_inspector_cpu_profile",

	"jvm.async_profile", "jvm.jcmd_gc", "jvm.jcmd_thread_dump", "jvm.jstat_gc",

	"py.spy_dump", "py.spy_top_snapshot",

	"gpu.nvidia_smi", "gpu.dcgm_metrics",

	"ebpf.biolatency", "ebpf.tcptop", "ebpf.tcprtt", "ebpf.execsnoop",
	"ebpf.bpftrace_oneliner",

	"change.recent",

	"metric.container_stats",
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

// TestSkillToolRefs_SuppressesSkippedGroups pins the noise-reduction rule
// that landed alongside this PR: built-in skills routinely reference tools
// in groups that may be skipped on the operator's environment (no Loki, no
// Tempo, no Argo, …). Surfacing those as validation errors on every startup
// is wall-of-text noise the operator cannot act on; the skipped-group banner
// already says the group was dropped.
//
// User typos in the SAME tool ('k8s' group is wired, so `k8s.fake_tool` is
// still a real bug) must continue to surface. That asymmetry is the whole
// point of this rule.
func TestSkillToolRefs_SuppressesSkippedGroups(t *testing.T) {
	t.Parallel()

	reg := skills.New([]*skills.Skill{
		{
			Name:         "incident-context",
			Description:  "shared infra skill that wants alerts + gitops",
			AllowedTools: []string{"alert.list_active", "gitops.argo_list_apps"},
			SystemPrompt: "irrelevant",
		},
		{
			Name:         "user-typo",
			Description:  "user skill with a real typo in a wired group",
			AllowedTools: []string{"k8s.fake_tool"},
			SystemPrompt: "irrelevant",
		},
	})

	tr := tools.New()
	tr.MustRegister(stubTool{name: "k8s.list_pods"})
	// alert + gitops are NOT registered; declare them skipped so the
	// validator should keep quiet about `alert.list_active` and
	// `gitops.argo_list_apps` while still flagging the k8s typo.
	tr.MarkSkipped("alert", "no Alertmanager endpoint configured")
	tr.MarkSkipped("gitops", "no Argo CD endpoint configured")

	err := wiring.ValidateSkillToolRefs(reg, tr)
	if err == nil {
		t.Fatal("expected validation error for k8s.fake_tool; got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "k8s.fake_tool") {
		t.Errorf("expected the k8s typo to surface; got: %s", msg)
	}
	for _, suppressed := range []string{"alert.list_active", "gitops.argo_list_apps"} {
		if strings.Contains(msg, suppressed) {
			t.Errorf("expected tool %q in a skipped group to be suppressed; got: %s", suppressed, msg)
		}
	}
}

// TestSkillToolRefs_SuppressesSubGroupSkips covers the case where a single
// tool group registers tools under one prefix (e.g. `perf.*`) but calls
// MarkSkipped with sub-group keys (`perf-pprof`, `perf-v8`, `perf-linux`).
// A naive `skipped[groupPrefix(toolName)]` lookup misses those — the tool's
// prefix is `perf` while the skip key is `perf-pprof`. The suppression rule
// must understand the sub-group shape or the wall-of-warnings comes back
// for environments that skipped one of the perf probes.
func TestSkillToolRefs_SuppressesSubGroupSkips(t *testing.T) {
	t.Parallel()

	reg := skills.New([]*skills.Skill{{
		Name:         "uses-perf-tools",
		Description:  "skill that references perf tools whose probes were skipped",
		AllowedTools: []string{"perf.go_pprof_cpu", "perf.linux_perf_record"},
		SystemPrompt: "irrelevant",
	}})

	tr := tools.New()
	// No perf tools registered. Skip keys mirror real
	// internal/tools/perf/register.go behaviour.
	tr.MarkSkipped("perf-pprof", "no pprof endpoints configured")
	tr.MarkSkipped("perf-linux", "perf requires linux, host is darwin")

	if err := wiring.ValidateSkillToolRefs(reg, tr); err != nil {
		t.Fatalf("expected sub-group skipped refs to be suppressed, got: %v", err)
	}
}
