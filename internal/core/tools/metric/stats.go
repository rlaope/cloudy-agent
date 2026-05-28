package metric

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// containerMetric is the computed, render-ready resource sample for one
// container. Percentages are absent (false) when the underlying counters do
// not permit a meaningful value (e.g. a zero system-CPU delta on a one-shot
// sample, or an unset memory limit).
type containerMetric struct {
	Name string

	CPUPercent    float64
	CPUPercentOK  bool
	MemUsage      uint64
	MemLimit      uint64
	MemPercent    float64
	MemPercentOK  bool
	NetRxBytes    uint64
	NetTxBytes    uint64
	BlkReadBytes  uint64
	BlkWriteBytes uint64
}

// computeMetric derives a containerMetric from a one-shot StatsResponse. All
// arithmetic is in pure helpers so it is unit-testable without a daemon.
func computeMetric(name string, s container.StatsResponse) containerMetric {
	cpu, cpuOK := cpuPercent(s)
	memUsage, memLimit, memPct, memOK := memory(s)
	rx, tx := network(s)
	read, write := blockIO(s)
	return containerMetric{
		Name:          name,
		CPUPercent:    cpu,
		CPUPercentOK:  cpuOK,
		MemUsage:      memUsage,
		MemLimit:      memLimit,
		MemPercent:    memPct,
		MemPercentOK:  memOK,
		NetRxBytes:    rx,
		NetTxBytes:    tx,
		BlkReadBytes:  read,
		BlkWriteBytes: write,
	}
}

// cpuPercent computes CPU utilisation as
//
//	(cpu_delta / system_delta) * online_cpus * 100
//
// using the current vs. previous sample carried in the one-shot response. It
// returns (0, false) when system_delta <= 0 (the divide-by-zero guard) — which
// is the common case on the very first one-shot sample where precpu is empty.
func cpuPercent(s container.StatsResponse) (float64, bool) {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if systemDelta <= 0 || cpuDelta < 0 {
		return 0, false
	}
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		// Older daemons omit online_cpus; fall back to the per-core array.
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return (cpuDelta / systemDelta) * cpus * 100, true
}

// memory returns usage, limit, and percent (usage/limit*100). Percent is OK
// only when limit > 0. Usage subtracts the cache component when present, which
// matches `docker stats`' reported figure on cgroup v1 hosts.
func memory(s container.StatsResponse) (usage, limit uint64, percent float64, ok bool) {
	usage = s.MemoryStats.Usage
	if cache, has := s.MemoryStats.Stats["cache"]; has && cache <= usage {
		usage -= cache
	}
	limit = s.MemoryStats.Limit
	if limit == 0 {
		return usage, limit, 0, false
	}
	return usage, limit, float64(usage) / float64(limit) * 100, true
}

// network sums received and transmitted bytes across every interface.
func network(s container.StatsResponse) (rx, tx uint64) {
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return rx, tx
}

// blockIO sums bytes read from and written to block devices, derived from the
// recursive io_service_bytes counters. Op casing varies across daemons, so the
// comparison is case-insensitive.
func blockIO(s container.StatsResponse) (read, write uint64) {
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			read += e.Value
		case "write":
			write += e.Value
		}
	}
	return read, write
}

// renderStats formats the computed rows as one line per container:
//
//	name | CPU% | mem used/limit (mem%) | net rx/tx | blkio r/w
//
// A header line reports the match count; per-container stats failures are
// appended as a short note so a partial result is still actionable.
func renderStats(workload string, matched int, rows []containerMetric, failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d container(s) for %q\n", matched, workload)
	if matched == 0 {
		return strings.TrimRight(b.String(), "\n")
	}
	for _, m := range rows {
		fmt.Fprintf(&b, "%s | CPU %s | mem %s/%s (%s) | net %s/%s | blkio %s/%s\n",
			m.Name,
			formatPercent(m.CPUPercent, m.CPUPercentOK),
			formatBytes(m.MemUsage), formatBytes(m.MemLimit), formatPercent(m.MemPercent, m.MemPercentOK),
			formatBytes(m.NetRxBytes), formatBytes(m.NetTxBytes),
			formatBytes(m.BlkReadBytes), formatBytes(m.BlkWriteBytes))
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "note: %d container(s) failed: %s\n", len(failures), strings.Join(failures, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatPercent renders a percent to two decimals, or "n/a" when not OK.
func formatPercent(p float64, ok bool) string {
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", p)
}

// formatBytes renders a byte count in the largest unit that keeps the mantissa
// below 1024, using base-1024 (KiB/MiB/…) like `docker stats`.
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
