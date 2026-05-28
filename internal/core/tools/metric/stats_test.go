package metric

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// statsWithCPU builds a StatsResponse exercising the CPU delta calculation.
func statsWithCPU(total, preTotal, system, preSystem uint64, online uint32) container.StatsResponse {
	var s container.StatsResponse
	s.CPUStats.CPUUsage.TotalUsage = total
	s.CPUStats.SystemUsage = system
	s.CPUStats.OnlineCPUs = online
	s.PreCPUStats.CPUUsage.TotalUsage = preTotal
	s.PreCPUStats.SystemUsage = preSystem
	return s
}

func TestCPUPercent(t *testing.T) {
	t.Run("normal calc", func(t *testing.T) {
		// cpu_delta=200, system_delta=1000, 4 cpus → 0.2*4*100 = 80%.
		s := statsWithCPU(300, 100, 2000, 1000, 4)
		got, ok := cpuPercent(s)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if got != 80 {
			t.Errorf("CPU%% = %v, want 80", got)
		}
	})

	t.Run("divide-by-zero guard (zero system delta)", func(t *testing.T) {
		s := statsWithCPU(300, 100, 1000, 1000, 4) // system_delta == 0
		got, ok := cpuPercent(s)
		if ok {
			t.Error("expected ok=false when system_delta <= 0")
		}
		if got != 0 {
			t.Errorf("CPU%% = %v, want 0", got)
		}
	})

	t.Run("first one-shot sample (empty precpu) is n/a", func(t *testing.T) {
		// One-shot first sample: precpu is zero, so system_delta == system,
		// but a fresh sample also has cpu==system semantics — guard on the
		// realistic zero precpu/system case.
		s := statsWithCPU(500, 0, 0, 0, 2) // system_delta == 0 → guarded
		if _, ok := cpuPercent(s); ok {
			t.Error("expected ok=false on zero system usage")
		}
	})

	t.Run("online_cpus falls back to percpu length", func(t *testing.T) {
		s := statsWithCPU(300, 100, 2000, 1000, 0)       // online=0
		s.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2} // 2 cores
		got, ok := cpuPercent(s)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if got != 40 { // 0.2 * 2 * 100
			t.Errorf("CPU%% = %v, want 40", got)
		}
	})
}

func TestMemory(t *testing.T) {
	t.Run("normal calc with cache subtraction", func(t *testing.T) {
		var s container.StatsResponse
		s.MemoryStats.Usage = 200
		s.MemoryStats.Limit = 1000
		s.MemoryStats.Stats = map[string]uint64{"cache": 50}
		usage, limit, pct, ok := memory(s)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if usage != 150 {
			t.Errorf("usage = %d, want 150 (200-50 cache)", usage)
		}
		if limit != 1000 {
			t.Errorf("limit = %d, want 1000", limit)
		}
		if pct != 15 { // 150/1000*100
			t.Errorf("mem%% = %v, want 15", pct)
		}
	})

	t.Run("missing limit yields not-ok", func(t *testing.T) {
		var s container.StatsResponse
		s.MemoryStats.Usage = 200 // Limit == 0
		usage, limit, pct, ok := memory(s)
		if ok {
			t.Error("expected ok=false when limit is 0")
		}
		if usage != 200 || limit != 0 || pct != 0 {
			t.Errorf("got usage=%d limit=%d pct=%v", usage, limit, pct)
		}
	})

	t.Run("no cache field leaves usage untouched", func(t *testing.T) {
		var s container.StatsResponse
		s.MemoryStats.Usage = 200
		s.MemoryStats.Limit = 400
		usage, _, pct, ok := memory(s)
		if !ok || usage != 200 || pct != 50 {
			t.Errorf("got usage=%d pct=%v ok=%v", usage, pct, ok)
		}
	})
}

func TestNetwork(t *testing.T) {
	var s container.StatsResponse
	s.Networks = map[string]container.NetworkStats{
		"eth0": {RxBytes: 100, TxBytes: 50},
		"eth1": {RxBytes: 1, TxBytes: 2},
	}
	rx, tx := network(s)
	if rx != 101 || tx != 52 {
		t.Errorf("rx=%d tx=%d, want 101/52", rx, tx)
	}
}

func TestBlockIO(t *testing.T) {
	var s container.StatsResponse
	s.BlkioStats.IoServiceBytesRecursive = []container.BlkioStatEntry{
		{Op: "Read", Value: 100},
		{Op: "write", Value: 200}, // mixed casing
		{Op: "Sync", Value: 999},  // ignored
	}
	read, write := blockIO(s)
	if read != 100 || write != 200 {
		t.Errorf("read=%d write=%d, want 100/200", read, write)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		0:               "0B",
		512:             "512B",
		1024:            "1.0KiB",
		1536:            "1.5KiB",
		1024 * 1024:     "1.0MiB",
		3 * 1024 * 1024: "3.0MiB",
	}
	for in, want := range cases {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	if got := formatPercent(12.345, true); got != "12.35%" {
		t.Errorf("formatPercent = %q, want 12.35%%", got)
	}
	if got := formatPercent(0, false); got != "n/a" {
		t.Errorf("formatPercent not-ok = %q, want n/a", got)
	}
}

func TestRenderStats(t *testing.T) {
	rows := []containerMetric{
		{
			Name: "web", CPUPercent: 80, CPUPercentOK: true,
			MemUsage: 150 * 1024 * 1024, MemLimit: 1024 * 1024 * 1024, MemPercent: 14.6, MemPercentOK: true,
			NetRxBytes: 1024, NetTxBytes: 2048, BlkReadBytes: 4096, BlkWriteBytes: 0,
		},
	}
	out := renderStats("web", 1, rows, nil)
	if !strings.Contains(out, "1 container(s) for \"web\"") {
		t.Errorf("missing header: %s", out)
	}
	if !strings.Contains(out, "web | CPU 80.00%") {
		t.Errorf("missing CPU line: %s", out)
	}
	if !strings.Contains(out, "net 1.0KiB/2.0KiB") {
		t.Errorf("missing net: %s", out)
	}

	t.Run("zero matches", func(t *testing.T) {
		got := renderStats("nope", 0, nil, nil)
		if !strings.Contains(got, "0 container(s)") || strings.Contains(got, "|") {
			t.Errorf("zero-match render unexpected: %s", got)
		}
	})

	t.Run("failure note", func(t *testing.T) {
		got := renderStats("web", 2, rows, []string{"db: boom"})
		if !strings.Contains(got, "note: 1 container(s) failed: db: boom") {
			t.Errorf("missing failure note: %s", got)
		}
	})
}
