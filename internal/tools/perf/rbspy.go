package perf

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/rlaope/cloudy/internal/tools"
)

// rbspyMaxOutput caps the rbspy subprocess output captured in a single call.
const rbspyMaxOutput = 1 << 20 // 1 MiB

// rbspyRunner is a function variable so tests can stub it out without
// shelling out to a real binary.
var rbspyRunner = runRBSpy

// runRBSpy executes the rbspy binary with a fixed argv vector — args is
// never built by concatenating user input. Returns stdout, stderr, and
// any exec error.
func runRBSpy(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "rbspy", args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	out := outBuf.Bytes()
	if len(out) > rbspyMaxOutput {
		out = out[:rbspyMaxOutput]
	}
	stdout = string(out)
	stderr = errBuf.String()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, fmt.Errorf("rbspy exited %d: %s", exitErr.ExitCode(), stderr)
		}
		return stdout, stderr, fmt.Errorf("exec rbspy: %w", runErr)
	}
	return stdout, stderr, nil
}

// newRBSpyDumpTool wraps `rbspy dump --pid PID [--time SECONDS]`. dump is
// rbspy's instantaneous backtrace command — it samples the target Ruby
// process briefly without attaching a persistent profiler.
func newRBSpyDumpTool() tools.Tool {
	type args struct {
		PID    int `json:"pid"`
		Time   int `json:"time_seconds"`
		Native int `json:"native"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the Ruby process to sample.",
				"minimum":     1,
			},
			"time_seconds": map[string]any{
				"type":        "integer",
				"description": "How long to sample before dumping (default 1, max 30).",
				"default":     1,
				"minimum":     1,
				"maximum":     30,
			},
			"native": map[string]any{
				"type":        "integer",
				"description": "1 = include native (C) stack frames; 0 = Ruby only (default).",
				"default":     0,
				"minimum":     0,
				"maximum":     1,
			},
		},
		"required": []string{"pid"},
	})
	return tools.Spec[args]{
		Name:        "perf.rbspy_dump",
		Description: "Sample a running Ruby process with rbspy and return the current backtrace.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.PID < 1 {
				return tools.Observation{}, fmt.Errorf("perf.rbspy_dump: pid must be >= 1")
			}
			if a.Time <= 0 {
				a.Time = 1
			}
			if a.Time > 30 {
				a.Time = 30
			}
			cliArgs := []string{
				"dump",
				"--pid", strconv.Itoa(a.PID),
				"--time", strconv.Itoa(a.Time),
			}
			if a.Native == 1 {
				cliArgs = append(cliArgs, "--native")
			}
			out, _, err := rbspyRunner(ctx, cliArgs...)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.rbspy_dump: %w", err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}
