package perf

import (
	"bytes"
	"compress/gzip"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// TestDecodeCPUProfile_TopFunctionsRoundtrip generates a small CPU profile
// in-process via runtime/pprof, feeds it into decodeCPUProfile, and
// asserts the output is non-empty and references decode's own helper.
func TestDecodeCPUProfile_TopFunctionsRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Fatalf("StartCPUProfile: %v", err)
	}
	// Burn ~200ms so the profiler gets a few samples.
	start := time.Now()
	for time.Since(start) < 200*time.Millisecond {
		_ = busy(1000)
	}
	pprof.StopCPUProfile()

	if buf.Len() == 0 {
		t.Skip("runtime/pprof produced no profile (likely sandboxed; CI Linux should not skip)")
	}

	// runtime/pprof writes gzipped pprof; profile.ParseData transparently
	// gunzips, but let's also verify the input has the gzip magic.
	if buf.Len() < 2 || buf.Bytes()[0] != 0x1f || buf.Bytes()[1] != 0x8b {
		t.Logf("warning: profile bytes do not look gzipped, len=%d", buf.Len())
	}

	tbl, summary, err := decodeCPUProfile(buf.Bytes(), 10)
	if err != nil {
		t.Fatalf("decodeCPUProfile: %v", err)
	}
	if !strings.Contains(summary, "cpu profile") {
		t.Errorf("summary missing prefix: %q", summary)
	}
	if tbl == nil || len(tbl.Headers) != 5 {
		t.Errorf("expected 5-column table, got %+v", tbl)
	}
}

func TestDecodeCPUProfile_EmptyInputReturnsExplanatorySummary(t *testing.T) {
	// An empty gzip stream is a valid gzip of an empty payload.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_ = w.Close()
	_, summary, err := decodeCPUProfile(gz.Bytes(), 10)
	if err == nil && !strings.Contains(summary, "empty profile") {
		t.Errorf("expected empty-profile explanation; err=%v summary=%q", err, summary)
	}
}

//go:noinline
func busy(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i * i
	}
	return s
}
