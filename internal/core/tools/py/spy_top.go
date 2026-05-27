package py

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// SpyTopTool implements py.spy_top_snapshot.
type SpyTopTool struct{}

func NewSpyTopTool() *SpyTopTool { return &SpyTopTool{} }

func (t *SpyTopTool) Name() string { return "py.spy_top_snapshot" }
func (t *SpyTopTool) Description() string {
	return "Run py-spy top snapshot on a local Python process. Returns a table of top functions by CPU time."
}
func (t *SpyTopTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the local Python process.",
				"minimum":     1,
			},
			"duration_seconds": map[string]any{
				"type":        "integer",
				"description": "Sampling duration in seconds (default: 5, max: 30).",
				"default":     5,
				"minimum":     1,
				"maximum":     30,
			},
		},
		"required": []string{"pid"},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *SpyTopTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID             int `json:"pid"`
		DurationSeconds int `json:"duration_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("py.spy_top_snapshot: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("py.spy_top_snapshot: pid must be >= 1")
	}
	if a.DurationSeconds <= 0 {
		a.DurationSeconds = 5
	}
	if a.DurationSeconds > 30 {
		a.DurationSeconds = 30
	}

	pid := strconv.Itoa(a.PID)
	dur := strconv.Itoa(a.DurationSeconds)

	// Try JSON format first; fall back to text if py-spy doesn't support it.
	out, _, err := runner(ctx, "py-spy", "top", "--duration", dur, "--pid", pid, "--format", "json")
	if err != nil {
		// Fall back to text output (older py-spy versions).
		out, _, err = runner(ctx, "py-spy", "top", "--duration", dur, "--pid", pid)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("py.spy_top_snapshot: %w", err)
		}
		tbl := parseSpyTopText(out)
		text := fmt.Sprintf("py-spy top pid=%d duration=%ds\n%s", a.PID, a.DurationSeconds, out)
		return tools.Observation{Text: text, Table: tbl, Raw: out}, nil
	}

	tbl, text := parseSpyTopJSON(out, a.PID, a.DurationSeconds)
	return tools.Observation{Text: text, Table: tbl, Raw: out}, nil
}

// spyTopJSONEntry is one function entry from py-spy top --format json.
type spyTopJSONEntry struct {
	FunctionName string  `json:"function_name"`
	FileName     string  `json:"filename"`
	Line         int     `json:"line"`
	OwnTime      float64 `json:"own_time"`
	TotalTime    float64 `json:"total_time"`
	OwnPercent   float64 `json:"own_percent"`
	TotalPercent float64 `json:"total_percent"`
}

func parseSpyTopJSON(output string, pid, dur int) (*render.Table, string) {
	output = strings.TrimSpace(output)
	tbl := &render.Table{
		Headers: []string{"%OWN", "%TOTAL", "FUNCTION", "FILE:LINE"},
		Aligns: []render.Align{
			render.AlignRight,
			render.AlignRight,
			render.AlignLeft,
			render.AlignLeft,
		},
	}

	var entries []spyTopJSONEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		// Not valid JSON — fall back to text parse.
		return parseSpyTopText(output), fmt.Sprintf("py-spy top pid=%d duration=%ds\n%s", pid, dur, output)
	}

	for _, e := range entries {
		tbl.Rows = append(tbl.Rows, []string{
			fmt.Sprintf("%.1f%%", e.OwnPercent),
			fmt.Sprintf("%.1f%%", e.TotalPercent),
			e.FunctionName,
			fmt.Sprintf("%s:%d", e.FileName, e.Line),
		})
	}

	text := fmt.Sprintf("py-spy top pid=%d duration=%ds rows=%d", pid, dur, len(entries))
	return tbl, text
}

// parseSpyTopText parses text-format py-spy top output.
// Expected columns (space-separated after trimming): OwnTime TotalTime Function File:Line
// Example line: "  5.00%   10.00%  compute  app.py:42"
func parseSpyTopText(output string) *render.Table {
	tbl := &render.Table{
		Headers: []string{"%OWN", "%TOTAL", "FUNCTION", "FILE:LINE"},
		Aligns: []render.Align{
			render.AlignRight,
			render.AlignRight,
			render.AlignLeft,
			render.AlignLeft,
		},
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%") || strings.HasPrefix(line, "---") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// Heuristic: first two fields contain "%" and look like percentages.
		if !strings.Contains(fields[0], "%") {
			continue
		}
		own := fields[0]
		total := fields[1]
		fn := fields[2]
		fileLine := fields[3]
		tbl.Rows = append(tbl.Rows, []string{own, total, fn, fileLine})
	}
	return tbl
}
