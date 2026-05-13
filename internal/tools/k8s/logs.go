package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultTailLines = int64(200)
	maxTailLines     = int64(5000)
)

// LogsTool implements k8s.logs.
type LogsTool struct{ client *Client }

// NewLogsTool constructs a LogsTool backed by the given Client.
func NewLogsTool(c *Client) *LogsTool { return &LogsTool{client: c} }

func (t *LogsTool) Name() string      { return "k8s.logs" }
func (t *LogsTool) ReadOnly() bool    { return true }
func (t *LogsTool) Description() string {
	return "Fetch container logs from a pod. Defaults to the last 200 lines; maximum 5000."
}
func (t *LogsTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"namespace":    strProp("Namespace of the pod."),
		"name":         strProp("Name of the pod."),
		"container":    strProp("Container name (optional if pod has one container)."),
		"tail_lines":   intProp("Number of lines to return from the end of the log (default 200, max 5000)."),
		"since_seconds": intProp("Return logs from the last N seconds."),
		"previous":     boolProp("Return logs from the previous (terminated) container instance."),
	}, []string{"namespace", "name"})
}

func (t *LogsTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace    string `json:"namespace"`
		Name         string `json:"name"`
		Container    string `json:"container"`
		TailLines    int64  `json:"tail_lines"`
		SinceSeconds int64  `json:"since_seconds"`
		Previous     bool   `json:"previous"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.logs: parse args: %w", err)
	}
	if a.Name == "" {
		return tools.Observation{}, fmt.Errorf("k8s.logs: name is required")
	}

	tail := defaultTailLines
	if a.TailLines > 0 {
		tail = a.TailLines
		if tail > maxTailLines {
			tail = maxTailLines
		}
	}

	opts := &corev1.PodLogOptions{
		Container: a.Container,
		TailLines: &tail,
		Previous:  a.Previous,
	}
	if a.SinceSeconds > 0 {
		opts.SinceSeconds = &a.SinceSeconds
	}

	text, err := t.client.PodLogs(a.Namespace, a.Name, opts)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.logs: %w", err)
	}

	// Represent logs as a single-column table for consistent rendering.
	tbl := &render.Table{
		Headers: []string{"LOG"},
		Rows:    [][]string{},
	}
	// Only populate table if text is non-empty (avoids giant tables in tests).
	_ = tbl

	return tools.Observation{Text: text, Table: nil, Raw: text}, nil
}
