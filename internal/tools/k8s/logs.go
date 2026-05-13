package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultTailLines = int64(200)
	maxTailLines     = int64(5000)
)

// LogsTool implements k8s.logs.
type LogsTool struct{ hub *Hub }

// NewLogsTool constructs a LogsTool backed by the given Hub.
func NewLogsTool(h *Hub) *LogsTool { return &LogsTool{hub: h} }

func (t *LogsTool) Name() string   { return "k8s.logs" }
func (t *LogsTool) ReadOnly() bool { return true }
func (t *LogsTool) Description() string {
	return "Fetch container logs from a pod. Defaults to the last 200 lines; maximum 5000."
}
func (t *LogsTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{
		"namespace":     strProp("Namespace of the pod."),
		"name":          strProp("Name of the pod."),
		"container":     strProp("Container name (optional if pod has one container)."),
		"tail_lines":    intProp("Number of lines to return from the end of the log (default 200, max 5000)."),
		"since_seconds": intProp("Return logs from the last N seconds."),
		"previous":      boolProp("Return logs from the previous (terminated) container instance."),
	}), []string{"namespace", "name"})
}

func (t *LogsTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace    string `json:"namespace"`
		Name         string `json:"name"`
		Container    string `json:"container"`
		TailLines    int64  `json:"tail_lines"`
		SinceSeconds int64  `json:"since_seconds"`
		Previous     bool   `json:"previous"`
		Context      string `json:"context"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.logs: parse args: %w", err)
	}
	if a.Name == "" {
		return tools.Observation{}, fmt.Errorf("k8s.logs: name is required")
	}

	if err := t.hub.CheckNamespace(a.Namespace); err != nil {
		return tools.Observation{Text: err.Error()}, nil
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.logs: %w", err)
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

	text, err := client.PodLogs(a.Namespace, a.Name, opts)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.logs: %w", err)
	}

	return tools.Observation{Text: text, Table: nil, Raw: text}, nil
}
