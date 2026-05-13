package ebpf

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/rlaope/cloudy/internal/tools"
)

// bccDurationArg is a shared schema fragment for the duration_seconds
// argument every BCC wrapper exposes.
var bccDurationArg = map[string]any{
	"type":        "integer",
	"description": "How long (seconds) to run the BCC tool before stopping (default 5, max 60).",
	"default":     5,
	"minimum":     1,
	"maximum":     60,
}

// boundDuration clamps a requested duration_seconds to the supported range.
func boundDuration(n int) int {
	if n <= 0 {
		return 5
	}
	if n > 60 {
		return 60
	}
	return n
}

// runWithDeadline invokes runner with an extra context deadline so a
// non-cooperating BCC binary cannot run past the user-requested duration.
// Many BCC tools accept an "interval count" pair as positional argv; the
// deadline is a defense against tools that ignore those (or that ship a
// signal-handling bug).
func runWithDeadline(ctx context.Context, dur int, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(dur+5)*time.Second)
	defer cancel()
	out, _, err := runner(ctx, bin, args...)
	return out, err
}

func newBiolatencyTool(bin string) tools.Tool {
	type args struct {
		Duration int `json:"duration_seconds"`
	}
	return tools.Spec[args]{
		Name:        "ebpf.biolatency",
		Description: "Run BCC biolatency for the configured duration to print a block I/O latency histogram (per-device when possible).",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"duration_seconds": bccDurationArg},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			d := boundDuration(a.Duration)
			out, err := runWithDeadline(ctx, d, bin, strconv.Itoa(d), "1")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ebpf.biolatency: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

func newTcpTopTool(bin string) tools.Tool {
	type args struct {
		Duration int `json:"duration_seconds"`
	}
	return tools.Spec[args]{
		Name:        "ebpf.tcptop",
		Description: "Run BCC tcptop for the configured duration to surface top TCP throughput by PID.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"duration_seconds": bccDurationArg},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			d := boundDuration(a.Duration)
			// tcptop's interval/count order is [interval count] — sample once
			// over the chosen duration.
			out, err := runWithDeadline(ctx, d, bin, strconv.Itoa(d), "1")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ebpf.tcptop: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

func newTcpRTTTool(bin string) tools.Tool {
	type args struct {
		Duration int `json:"duration_seconds"`
	}
	return tools.Spec[args]{
		Name:        "ebpf.tcprtt",
		Description: "Run BCC tcprtt for the configured duration to print a TCP round-trip-time histogram.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"duration_seconds": bccDurationArg},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			d := boundDuration(a.Duration)
			out, err := runWithDeadline(ctx, d, bin, "-i", strconv.Itoa(d), "1")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ebpf.tcprtt: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

func newExecsnoopTool(bin string) tools.Tool {
	type args struct {
		Duration int `json:"duration_seconds"`
	}
	return tools.Spec[args]{
		Name:        "ebpf.execsnoop",
		Description: "Run BCC execsnoop for the configured duration to trace exec() syscalls — useful for surfacing unexpected child processes.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"duration_seconds": bccDurationArg},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			d := boundDuration(a.Duration)
			// execsnoop streams continuously; rely on the runWithDeadline
			// timeout to stop it after dur+5s grace.
			out, err := runWithDeadline(ctx, d, bin, "-T")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ebpf.execsnoop: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

var mustJSON = tools.MustJSON
