package perf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// perfRunner is a function variable so tests can stub out the Linux
// `perf` binary without invoking real kernel sampling.
var perfRunner = runPerfPipeline

// linuxPerfSupported reports whether Linux perf is even attempt-able on
// this host. Like the ebpf gate, this guards both registration and a
// helpful skipped-reason in /tools.
func linuxPerfSupported() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("perf.linux_perf_record requires linux, host is %s", runtime.GOOS)
	}
	bin, err := exec.LookPath("perf")
	if err != nil {
		return "", fmt.Errorf("perf binary not on PATH")
	}
	return bin, nil
}

// runPerfPipeline executes `perf record` writing to a tempdir-scoped file
// and then `perf report --stdio` to render that file. The two steps are
// chained because most LLM-friendly output comes from report, not the raw
// perf.data binary.
func runPerfPipeline(ctx context.Context, bin string, pid, durationSecs, freqHz int) (string, error) {
	dir, err := os.MkdirTemp("", "cloudy-perf-")
	if err != nil {
		return "", fmt.Errorf("mktemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "perf.data")

	recordArgs := []string{
		"record", "-q",
		"-F", strconv.Itoa(freqHz),
		"-g",
		"-o", out,
		"-p", strconv.Itoa(pid),
		"--", "sleep", strconv.Itoa(durationSecs),
	}
	ctxR, cancelR := context.WithTimeout(ctx, time.Duration(durationSecs+10)*time.Second)
	defer cancelR()
	rec := exec.CommandContext(ctxR, bin, recordArgs...)
	var recOut, recErr bytes.Buffer
	rec.Stdout = &recOut
	rec.Stderr = &recErr
	if err := rec.Run(); err != nil {
		return "", fmt.Errorf("perf record: %w (stderr: %s)", err, strings.TrimSpace(recErr.String()))
	}

	reportArgs := []string{
		"report", "--stdio",
		"-i", out,
		"-g", "graph,0.5,caller",
	}
	ctxP, cancelP := context.WithTimeout(ctx, 30*time.Second)
	defer cancelP()
	rep := exec.CommandContext(ctxP, bin, reportArgs...)
	var repOut, repErr bytes.Buffer
	rep.Stdout = &repOut
	rep.Stderr = &repErr
	if err := rep.Run(); err != nil {
		return "", fmt.Errorf("perf report: %w (stderr: %s)", err, strings.TrimSpace(repErr.String()))
	}
	return repOut.String(), nil
}

func newLinuxPerfRecordTool(bin string) tools.Tool {
	type args struct {
		PID       int `json:"pid"`
		Duration  int `json:"duration_seconds"`
		Frequency int `json:"frequency_hz"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid":              map[string]any{"type": "integer", "description": "PID to sample.", "minimum": 1},
			"duration_seconds": map[string]any{"type": "integer", "description": "Sample window (default 5, max 30).", "default": 5, "minimum": 1, "maximum": 30},
			"frequency_hz":     map[string]any{"type": "integer", "description": "Sample frequency in Hz (default 99 to avoid aliasing on 100Hz timers).", "default": 99, "minimum": 1, "maximum": 9999},
		},
		"required": []string{"pid"},
	})
	return tools.Spec[args]{
		Name:        "perf.linux_perf_record",
		Description: "Sample a Linux process with `perf record -g` for the configured duration, then render call-graph output via `perf report --stdio`.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.PID < 1 {
				return tools.Observation{}, fmt.Errorf("perf.linux_perf_record: pid must be >= 1")
			}
			if a.Duration <= 0 {
				a.Duration = 5
			}
			if a.Duration > 30 {
				a.Duration = 30
			}
			if a.Frequency <= 0 {
				a.Frequency = 99
			}
			if a.Frequency > 9999 {
				a.Frequency = 9999
			}
			out, err := perfRunner(ctx, bin, a.PID, a.Duration, a.Frequency)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.linux_perf_record: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}
