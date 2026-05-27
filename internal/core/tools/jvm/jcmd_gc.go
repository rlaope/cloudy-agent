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

// JcmdGCTool implements jvm.jcmd_gc.
type JcmdGCTool struct{}

func NewJcmdGCTool() *JcmdGCTool { return &JcmdGCTool{} }

func (t *JcmdGCTool) Name() string { return "jvm.jcmd_gc" }
func (t *JcmdGCTool) Description() string {
	return "Run jcmd GC.heap_info and GC.class_histogram on a local JVM process."
}
func (t *JcmdGCTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the local JVM process.",
				"minimum":     1,
			},
		},
		"required": []string{"pid"},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *JcmdGCTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_gc: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_gc: pid must be >= 1")
	}

	pid := strconv.Itoa(a.PID)

	heapOut, _, err := runner(ctx, "jcmd", pid, "GC.heap_info")
	if err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_gc: GC.heap_info: %w", err)
	}

	histOut, _, err := runner(ctx, "jcmd", pid, "GC.class_histogram")
	if err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_gc: GC.class_histogram: %w", err)
	}

	tbl, top20 := parseClassHistogram(histOut, 20)

	var sb strings.Builder
	sb.WriteString("=== GC.heap_info ===\n")
	sb.WriteString(heapOut)
	sb.WriteString("\n=== GC.class_histogram (top 20) ===\n")
	sb.WriteString(top20)

	return tools.Observation{
		Text:  sb.String(),
		Table: tbl,
		Raw:   map[string]string{"heap_info": heapOut, "class_histogram": histOut},
	}, nil
}

// parseClassHistogram parses the output of jcmd <pid> GC.class_histogram.
// It returns a Table of the top n classes and a text summary.
//
// jcmd histogram format (after header lines):
//
//	num     #instances         #bytes  class name
//	----------------------------------------------
//	   1:          1234       56789  java.lang.Object
func parseClassHistogram(output string, n int) (*render.Table, string) {
	tbl := &render.Table{
		Headers: []string{"NUM", "INSTANCES", "BYTES", "CLASS"},
		Aligns: []render.Align{
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
			render.AlignLeft,
		},
	}

	lines := strings.Split(output, "\n")
	count := 0
	var sb strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "num") || strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "Total") {
			continue
		}
		// Remove trailing colon from num field: "1:" -> "1"
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		numField := strings.TrimRight(fields[0], ":")
		instances := fields[1]
		bytes_ := fields[2]
		// Class name may include module info with spaces, e.g. "[B (java.base@17)"
		class := strings.Join(fields[3:], " ")

		if count < n {
			tbl.Rows = append(tbl.Rows, []string{numField, instances, bytes_, class})
			fmt.Fprintf(&sb, "%s\t%s\t%s\t%s\n", numField, instances, bytes_, class)
			count++
		}
	}

	return tbl, sb.String()
}
