package tools

// RiskLevel classifies a tool by how much it perturbs the system being
// observed. Read-only enforcement at the transport / kube-verb layer is
// necessary but not sufficient — some permitted reads still trigger
// stop-the-world pauses (jcmd class_histogram on a large heap), attach
// non-trivial probes (async-profiler, eBPF), or sample for many seconds
// (perf record). ApprovalHook uses RiskLevel to decide which calls require
// explicit operator consent before the agent dispatches them.
type RiskLevel int

const (
	// RiskUnknown is the zero value; treated as RiskLow by RiskOf.
	RiskUnknown RiskLevel = iota
	// RiskLow: cheap inspection, single resource lookups, paginated lists.
	RiskLow
	// RiskMedium: short profiling windows or wide-scope queries.
	RiskMedium
	// RiskHigh: STW pause, long profiling window, attached probe, or a
	// cluster-wide scan. ApprovalHook gates these behind explicit consent.
	RiskHigh
)

// String renders a RiskLevel for log / UI output.
func (r RiskLevel) String() string {
	switch r {
	case RiskHigh:
		return "high"
	case RiskMedium:
		return "medium"
	case RiskLow:
		return "low"
	default:
		return "unknown"
	}
}

// RiskRated is the optional interface tools may implement to advertise the
// system-impact of a single call. Tools that do not implement it fall back
// to riskByName, which carries the curated allowlist of known-perturbing
// tool names.
type RiskRated interface {
	Risk() RiskLevel
}

// RiskOf returns the rated level of t. The lookup order is:
//  1. the tool's own RiskRated.Risk() if implemented
//  2. the name-based allowlist (riskByName)
//  3. RiskLow
//
// Keeping the allowlist alongside the interface means new high-risk tools
// can be added in one place even before their package gets touched.
func RiskOf(t Tool) RiskLevel {
	if rr, ok := t.(RiskRated); ok {
		if lvl := rr.Risk(); lvl != RiskUnknown {
			return lvl
		}
	}
	return riskByName(t.Name())
}

// riskByName is the curated allowlist of tools known to cause STW pauses,
// require long sampling windows, or attach probes that distort the system.
// Mirrors the isProfileTool set used by LimitGuardHook plus jvm.jcmd_gc,
// whose class-histogram pass can stall a large heap.
func riskByName(name string) RiskLevel {
	switch name {
	case "jvm.async_profile",
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
		"ebpf.bpftrace_oneliner":
		return RiskHigh
	}
	return RiskLow
}
