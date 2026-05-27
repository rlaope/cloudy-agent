// Package ebpf provides read-only kernel-observability tools wrapping a
// curated subset of BCC's command-line tools and a small allowlist of
// bpftrace one-liners. There is no free-form bpftrace script input — the
// LLM picks from a fixed catalog of pre-vetted oneliners that do not modify
// kernel state.
//
// Why a catalog instead of arbitrary scripts? bpftrace happily accepts
// `system()` builtins and arbitrary tracepoint attach points; an LLM that
// can submit a script can attach any uprobe and cause meaningful overhead
// or even crash kernel paths on older kernels. The catalog model keeps
// cloudy's contract of "read-only kernel observation, no destructive
// possibility."
package ebpf

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// platformGate reports nil when the host is capable of running eBPF tools,
// or an error explaining why it cannot. The checks performed:
//   - OS is Linux (BCC/bpftrace are Linux-only).
//   - Effective UID is 0, OR /proc/self/status indicates CAP_BPF in CapEff.
//
// The capability bitmask for CAP_BPF is 39 (1 << 39 on the CapEff hex
// integer). Some older kernels and BCC builds also need CAP_SYS_ADMIN; we
// check it as an alternative because operators who can run BCC there will
// have one or the other.
func platformGate() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("ebpf tools require linux, host is %s", runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		return nil
	}
	if hasBPFCapability() {
		return nil
	}
	return errors.New("ebpf tools require root or CAP_BPF / CAP_SYS_ADMIN")
}

// hasBPFCapability parses /proc/self/status and reports whether CapEff
// includes CAP_BPF (39) or CAP_SYS_ADMIN (21). Returns false on any parse
// failure — we err on the side of "no capability."
func hasBPFCapability() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		bits, err := strconv.ParseUint(hex, 16, 64)
		if err != nil {
			return false
		}
		const capSysAdmin = uint64(1) << 21
		const capBPF = uint64(1) << 39
		return bits&(capBPF|capSysAdmin) != 0
	}
	return false
}

// resolveBinary returns the absolute path of name when it is on PATH, plus
// any well-known fallbacks specific to a given BCC tool. BCC ships its
// tools both as bare names (when packaged) and under /usr/share/bcc/tools.
// Returns an empty string if neither location contains the binary.
func resolveBinary(name string, fallbacks ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range fallbacks {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
