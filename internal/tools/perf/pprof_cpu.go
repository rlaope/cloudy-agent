package perf

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/google/pprof/profile"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// newPprofCPUTool wraps GET /debug/pprof/profile?seconds=N. Unlike the
// text variants, the CPU profile endpoint always returns a binary pprof
// protobuf — we decode it with google/pprof and return a top-N flat / cum
// table so the LLM never has to handle binary bytes.
func newPprofCPUTool(clients map[string]*PprofClient) tools.Tool {
	type args struct {
		Name     string `json:"name"`
		Duration int    `json:"duration_seconds"`
		TopN     int    `json:"top_n"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": pprofEndpointSchema,
			"duration_seconds": map[string]any{
				"type":        "integer",
				"description": "CPU sample window (default 5, max 60).",
				"default":     5, "minimum": 1, "maximum": 60,
			},
			"top_n": map[string]any{
				"type":        "integer",
				"description": "Number of top functions to return by flat samples (default 20, max 200).",
				"default":     20, "minimum": 1, "maximum": 200,
			},
		},
	})
	return tools.Spec[args]{
		Name:        "perf.go_pprof_cpu",
		Description: "Capture a Go CPU profile via /debug/pprof/profile?seconds=N and return the top-N functions by flat and cumulative sample count.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Duration <= 0 {
				a.Duration = 5
			}
			if a.Duration > 60 {
				a.Duration = 60
			}
			if a.TopN <= 0 {
				a.TopN = 20
			}
			if a.TopN > 200 {
				a.TopN = 200
			}
			c, err := pickPprof(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{"seconds": {strconv.Itoa(a.Duration)}}
			body, err := c.RawGet(ctx, "/debug/pprof/profile", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.go_pprof_cpu: %w", err)
			}
			tbl, summary, err := decodeCPUProfile(body, a.TopN)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.go_pprof_cpu: decode: %w", err)
			}
			return tools.Observation{Text: summary, Table: tbl, Raw: summary}, nil
		},
	}.Build()
}

// decodeCPUProfile parses a pprof binary and returns a top-N table by flat
// sample value. "Flat" = samples whose top frame is this function. "Cum" =
// samples that pass through this function (top frame or below). The total
// sample budget is the sum of every sample's first value (the CPU profile's
// canonical "samples" axis).
func decodeCPUProfile(body []byte, topN int) (*render.Table, string, error) {
	p, err := profile.ParseData(body)
	if err != nil {
		return nil, "", err
	}
	if len(p.Sample) == 0 {
		return nil, "(empty profile — no samples captured in window)", nil
	}

	// Profile.Sample[i].Value[0] is the canonical samples count for cpu
	// profiles produced by runtime/pprof. Cross-check via SampleType.
	valIdx := 0
	for i, st := range p.SampleType {
		if st.Type == "samples" {
			valIdx = i
			break
		}
	}

	type bucket struct {
		flat int64
		cum  int64
	}
	byFunc := map[string]*bucket{}
	var total int64
	for _, s := range p.Sample {
		if valIdx >= len(s.Value) {
			continue
		}
		v := s.Value[valIdx]
		total += v
		// Flat: top frame (first Location).
		if len(s.Location) > 0 && len(s.Location[0].Line) > 0 {
			top := s.Location[0].Line[0].Function.Name
			b := byFunc[top]
			if b == nil {
				b = &bucket{}
				byFunc[top] = b
			}
			b.flat += v
		}
		// Cum: every distinct function in the stack.
		seen := map[string]struct{}{}
		for _, loc := range s.Location {
			for _, line := range loc.Line {
				name := line.Function.Name
				if _, ok := seen[name]; ok {
					continue
				}
				seen[name] = struct{}{}
				b := byFunc[name]
				if b == nil {
					b = &bucket{}
					byFunc[name] = b
				}
				b.cum += v
			}
		}
	}

	type row struct {
		name string
		flat int64
		cum  int64
	}
	rows := make([]row, 0, len(byFunc))
	for n, b := range byFunc {
		rows = append(rows, row{n, b.flat, b.cum})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].flat > rows[j].flat })
	if len(rows) > topN {
		rows = rows[:topN]
	}

	tbl := &render.Table{
		Headers: []string{"FLAT", "FLAT%", "CUM", "CUM%", "FUNCTION"},
		Aligns:  []render.Align{render.AlignRight, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignLeft},
	}
	pct := func(v int64) string {
		if total == 0 {
			return "0.0%"
		}
		return fmt.Sprintf("%.1f%%", float64(v)*100/float64(total))
	}
	for _, r := range rows {
		tbl.Rows = append(tbl.Rows, []string{
			strconv.FormatInt(r.flat, 10),
			pct(r.flat),
			strconv.FormatInt(r.cum, 10),
			pct(r.cum),
			r.name,
		})
	}
	return tbl, fmt.Sprintf("cpu profile: %d samples, top %d by flat", total, len(rows)), nil
}
