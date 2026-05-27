package jvm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// JstatGCTool implements jvm.jstat_gc.
type JstatGCTool struct{}

func NewJstatGCTool() *JstatGCTool { return &JstatGCTool{} }

func (t *JstatGCTool) Name() string { return "jvm.jstat_gc" }
func (t *JstatGCTool) Description() string {
	return "Run jstat -gc on a local JVM process to monitor GC statistics over time."
}
func (t *JstatGCTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the local JVM process.",
				"minimum":     1,
			},
			"interval_ms": map[string]any{
				"type":        "integer",
				"description": "Sampling interval in milliseconds (default: 1000).",
				"default":     1000,
				"minimum":     100,
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of samples to collect (default: 5, max: 30).",
				"default":     5,
				"maximum":     30,
				"minimum":     1,
			},
		},
		"required": []string{"pid"},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *JstatGCTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID        int `json:"pid"`
		IntervalMs int `json:"interval_ms"`
		Count      int `json:"count"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jstat_gc: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("jvm.jstat_gc: pid must be >= 1")
	}
	if a.IntervalMs <= 0 {
		a.IntervalMs = 1000
	}
	if a.Count <= 0 {
		a.Count = 5
	}
	if a.Count > 30 {
		a.Count = 30
	}

	pid := strconv.Itoa(a.PID)
	interval := strconv.Itoa(a.IntervalMs)
	count := strconv.Itoa(a.Count)

	out, _, err := runner(ctx, "jstat", "-gc", pid, interval, count)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jstat_gc: %w", err)
	}

	tbl := parseJstatOutput(out)
	text := fmt.Sprintf("jstat -gc pid=%d interval=%dms count=%d\n%s", a.PID, a.IntervalMs, a.Count, out)

	return tools.Observation{
		Text:  text,
		Table: tbl,
		Raw:   out,
	}, nil
}

// parseJstatOutput parses tabular jstat -gc output.
// The first line contains space-separated column headers; subsequent lines are data rows.
func parseJstatOutput(output string) *render.Table {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return nil
	}

	tbl := &render.Table{}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if i == 0 {
			tbl.Headers = fields
			// Right-align all columns (numeric data).
			tbl.Aligns = make([]render.Align, len(fields))
			for j := range tbl.Aligns {
				tbl.Aligns[j] = render.AlignRight
			}
			continue
		}
		tbl.Rows = append(tbl.Rows, fields)
	}
	return tbl
}
