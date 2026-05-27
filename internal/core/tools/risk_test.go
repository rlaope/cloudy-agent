package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type riskNamedTool struct{ n string }

func (r riskNamedTool) Name() string            { return r.n }
func (r riskNamedTool) Description() string     { return "" }
func (r riskNamedTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (r riskNamedTool) Run(context.Context, json.RawMessage) (Observation, error) {
	return Observation{}, nil
}

type ratedTool struct {
	riskNamedTool
	level RiskLevel
}

func (r ratedTool) Risk() RiskLevel { return r.level }

func TestRiskOf_HighByName(t *testing.T) {
	high := []string{
		"jvm.async_profile",
		"jvm.jcmd_gc",
		"perf.linux_perf_record",
		"perf.v8_inspector_cpu_profile",
		"perf.go_pprof_cpu",
		"perf.rbspy_dump",
		"py.spy_top_snapshot",
		"py.spy_dump",
		"ebpf.biolatency",
		"ebpf.tcprtt",
		"ebpf.tcptop",
		"ebpf.execsnoop",
		"ebpf.bpftrace_oneliner",
	}
	for _, name := range high {
		if RiskOf(riskNamedTool{n: name}) != RiskHigh {
			t.Errorf("%s expected RiskHigh", name)
		}
	}
}

func TestRiskOf_LowByDefault(t *testing.T) {
	low := []string{
		"k8s.list_pods",
		"prom.query_range",
		"log.loki_query_range",
		"db.pg_stat_activity",
	}
	for _, name := range low {
		if RiskOf(riskNamedTool{n: name}) != RiskLow {
			t.Errorf("%s expected RiskLow", name)
		}
	}
}

func TestRiskOf_InterfaceOverride(t *testing.T) {
	// A tool whose name would default to Low can declare itself High.
	tool := ratedTool{riskNamedTool: riskNamedTool{n: "k8s.list_pods"}, level: RiskHigh}
	if RiskOf(tool) != RiskHigh {
		t.Fatalf("Risk() override ignored")
	}
}

func TestRiskOf_UnknownLevelFallsBackToName(t *testing.T) {
	// RiskUnknown from Risk() should fall through to riskByName.
	tool := ratedTool{riskNamedTool: riskNamedTool{n: "jvm.async_profile"}, level: RiskUnknown}
	if RiskOf(tool) != RiskHigh {
		t.Fatalf("RiskUnknown should fall through to name-based lookup")
	}
}

func TestRiskLevel_String(t *testing.T) {
	cases := map[RiskLevel]string{
		RiskUnknown: "unknown",
		RiskLow:     "low",
		RiskMedium:  "medium",
		RiskHigh:    "high",
	}
	for lvl, want := range cases {
		if got := lvl.String(); got != want {
			t.Errorf("RiskLevel(%d).String() = %q, want %q", lvl, got, want)
		}
	}
}
