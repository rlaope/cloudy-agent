package py

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/tools"
)

// SpyDumpTool implements py.spy_dump.
type SpyDumpTool struct{}

func NewSpyDumpTool() *SpyDumpTool { return &SpyDumpTool{} }

func (t *SpyDumpTool) Name() string { return "py.spy_dump" }
func (t *SpyDumpTool) Description() string {
	return "Run py-spy dump on a local Python process to capture a stack trace of all threads."
}
func (t *SpyDumpTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the local Python process.",
				"minimum":     1,
			},
		},
		"required": []string{"pid"},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *SpyDumpTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("py.spy_dump: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("py.spy_dump: pid must be >= 1")
	}

	out, _, err := runner(ctx, "py-spy", "dump", "--pid", strconv.Itoa(a.PID))
	if err != nil {
		return tools.Observation{}, fmt.Errorf("py.spy_dump: %w", err)
	}

	threadCount := countThreads(out)

	var sb strings.Builder
	fmt.Fprintf(&sb, "py-spy dump pid=%d  threads=%d\n\n", a.PID, threadCount)
	sb.WriteString(out)

	return tools.Observation{
		Text: sb.String(),
		Raw:  out,
	}, nil
}

// countThreads counts the number of thread sections in py-spy dump output.
// Each thread section starts with a "Thread" header line.
func countThreads(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Thread ") || strings.HasPrefix(trimmed, "GIL held by") {
			count++
		}
	}
	return count
}
