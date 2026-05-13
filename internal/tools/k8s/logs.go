package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultTailLines = int64(200)
	maxTailLines     = int64(5000)
)

type logsArgs struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Container    string `json:"container"`
	TailLines    int64  `json:"tail_lines"`
	SinceSeconds int64  `json:"since_seconds"`
	Previous     bool   `json:"previous"`
	Context      string `json:"context"`
}

// NewLogsTool returns the k8s.logs tool.
func NewLogsTool(hub *Hub) tools.Tool {
	return tools.Spec[logsArgs]{
		Name:        "k8s.logs",
		Description: "Fetch container logs from a pod. Defaults to the last 200 lines; maximum 5000.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":     strProp("Namespace of the pod."),
			"name":          strProp("Name of the pod."),
			"container":     strProp("Container name (optional if pod has one container)."),
			"tail_lines":    intProp("Number of lines to return from the end of the log (default 200, max 5000)."),
			"since_seconds": intProp("Return logs from the last N seconds."),
			"previous":      boolProp("Return logs from the previous (terminated) container instance."),
		}), []string{"namespace", "name"}),
		Run: func(_ context.Context, a logsArgs) (tools.Observation, error) {
			if a.Name == "" {
				return tools.Observation{}, fmt.Errorf("k8s.logs: name is required")
			}
			if err := hub.CheckNamespace(a.Namespace); err != nil {
				return tools.Observation{Text: err.Error()}, nil
			}
			client, err := hub.Get(a.Context)
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
			return tools.Observation{Text: text, Raw: text}, nil
		},
	}.Build()
}
