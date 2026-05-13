package ebpf

import (
	"sort"
	"strings"

	"github.com/rlaope/cloudy/internal/tools"
)

// RegisterAll adds every ebpf.* tool whose backing binary and capability
// gate are satisfied. When the platform gate fails (non-Linux, no CAP_BPF,
// no root) the entire ebpf group is marked skipped with a single composed
// reason — there is nothing useful to register in that environment.
//
// Per-binary skips: when the platform gate passes but a specific binary is
// missing (e.g. bpftrace not installed but BCC is), each missing binary
// records a per-tool skip reason while the rest still register.
func RegisterAll(reg *tools.Registry) {
	if err := platformGate(); err != nil {
		reg.MarkSkipped("ebpf", err.Error())
		return
	}

	missing := map[string]string{}

	if bin := resolveBinary("biolatency", "/usr/share/bcc/tools/biolatency"); bin != "" {
		reg.MustRegister(newBiolatencyTool(bin))
	} else {
		missing["biolatency"] = "binary not on PATH (tried bcc-tools locations)"
	}
	if bin := resolveBinary("tcptop", "/usr/share/bcc/tools/tcptop"); bin != "" {
		reg.MustRegister(newTcpTopTool(bin))
	} else {
		missing["tcptop"] = "binary not on PATH (tried bcc-tools locations)"
	}
	if bin := resolveBinary("tcprtt", "/usr/share/bcc/tools/tcprtt"); bin != "" {
		reg.MustRegister(newTcpRTTTool(bin))
	} else {
		missing["tcprtt"] = "binary not on PATH (tried bcc-tools locations)"
	}
	if bin := resolveBinary("execsnoop", "/usr/share/bcc/tools/execsnoop"); bin != "" {
		reg.MustRegister(newExecsnoopTool(bin))
	} else {
		missing["execsnoop"] = "binary not on PATH (tried bcc-tools locations)"
	}
	if bin := resolveBinary("bpftrace"); bin != "" {
		reg.MustRegister(newBpftraceOnelinerTool(bin))
	} else {
		missing["bpftrace_oneliner"] = "bpftrace binary not on PATH"
	}

	// If nothing registered, mark the whole group skipped with a composed
	// reason. Otherwise leave the group inventoried with whatever bound.
	if len(missing) == 5 {
		reg.MarkSkipped("ebpf", "no ebpf binaries available: "+strings.Join(missingValues(missing), "; "))
	}
}

// missingValues returns the reason strings from a map in stable order.
func missingValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+": "+m[k])
	}
	return out
}
