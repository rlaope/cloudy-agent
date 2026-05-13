package jvm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// JcmdThreadTool implements jvm.jcmd_thread_dump.
type JcmdThreadTool struct{}

func NewJcmdThreadTool() *JcmdThreadTool { return &JcmdThreadTool{} }

func (t *JcmdThreadTool) Name() string   { return "jvm.jcmd_thread_dump" }
func (t *JcmdThreadTool) ReadOnly() bool { return true }
func (t *JcmdThreadTool) Description() string {
	return "Run jcmd Thread.print on a local JVM process. Reports thread state counts and any deadlocks detected."
}
func (t *JcmdThreadTool) Schema() json.RawMessage {
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

func (t *JcmdThreadTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_thread_dump: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_thread_dump: pid must be >= 1")
	}

	pid := strconv.Itoa(a.PID)
	out, _, err := runner(ctx, "jcmd", pid, "Thread.print")
	if err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.jcmd_thread_dump: %w", err)
	}

	counts, deadlock := parseThreadDump(out)

	tbl := &render.Table{
		Headers: []string{"STATE", "COUNT"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignRight},
	}
	states := []string{"RUNNABLE", "WAITING", "TIMED_WAITING", "BLOCKED"}
	total := 0
	for _, s := range states {
		c := counts[s]
		total += c
		tbl.Rows = append(tbl.Rows, []string{s, strconv.Itoa(c)})
	}
	tbl.Rows = append(tbl.Rows, []string{"TOTAL", strconv.Itoa(total)})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Thread dump for PID %d\n", a.PID)
	for _, s := range states {
		fmt.Fprintf(&sb, "  %-15s %d\n", s, counts[s])
	}
	fmt.Fprintf(&sb, "  %-15s %d\n", "TOTAL", total)
	if deadlock != "" {
		sb.WriteString("\n--- DEADLOCK DETECTED ---\n")
		sb.WriteString(deadlock)
	}

	return tools.Observation{
		Text:  sb.String(),
		Table: tbl,
		Raw:   out,
	}, nil
}

// parseThreadDump scans jcmd Thread.print output and counts thread states.
// It also extracts the deadlock section if present.
func parseThreadDump(output string) (counts map[string]int, deadlock string) {
	counts = map[string]int{
		"RUNNABLE":      0,
		"WAITING":       0,
		"TIMED_WAITING": 0,
		"BLOCKED":       0,
	}

	lines := strings.Split(output, "\n")
	var deadlockLines []string
	inDeadlock := false

	for _, line := range lines {
		// Thread state line looks like: "   java.lang.Thread.State: RUNNABLE"
		if strings.Contains(line, "java.lang.Thread.State:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				state := strings.TrimSpace(parts[1])
				// State may have trailing info like "WAITING (on object monitor)"
				state = strings.Fields(state)[0]
				if _, ok := counts[state]; ok {
					counts[state]++
				}
			}
		}

		// Detect deadlock section.
		if strings.Contains(line, "Found one Java-level deadlock") ||
			strings.Contains(line, "Found") && strings.Contains(line, "deadlock") {
			inDeadlock = true
		}
		if inDeadlock {
			deadlockLines = append(deadlockLines, line)
		}
	}

	if len(deadlockLines) > 0 {
		deadlock = strings.Join(deadlockLines, "\n")
	}
	return counts, deadlock
}
