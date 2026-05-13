package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// DescribePodTool implements k8s.describe_pod.
type DescribePodTool struct{ hub *Hub }

// NewDescribePodTool constructs a DescribePodTool backed by the given Hub.
func NewDescribePodTool(h *Hub) *DescribePodTool { return &DescribePodTool{hub: h} }

func (t *DescribePodTool) Name() string   { return "k8s.describe_pod" }
func (t *DescribePodTool) ReadOnly() bool { return true }
func (t *DescribePodTool) Description() string {
	return "Return a structured summary of a pod: phase, containers, restart counts, recent events, IP, node."
}
func (t *DescribePodTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{
		"namespace": strProp("Namespace of the pod."),
		"name":      strProp("Name of the pod."),
	}), []string{"namespace", "name"})
}

func (t *DescribePodTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.describe_pod: parse args: %w", err)
	}
	if a.Name == "" {
		return tools.Observation{}, fmt.Errorf("k8s.describe_pod: name is required")
	}

	if err := t.hub.CheckNamespace(a.Namespace); err != nil {
		return tools.Observation{Text: err.Error()}, nil
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.describe_pod: %w", err)
	}

	pod, err := client.Pod(a.Namespace, a.Name)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.describe_pod: %w", err)
	}

	// Container table.
	cTbl := &render.Table{
		Headers: []string{"CONTAINER", "IMAGE", "READY", "RESTARTS", "STATE"},
	}
	for _, cs := range pod.Status.ContainerStatuses {
		state := containerState(cs.State) //nolint:govet
		cTbl.Rows = append(cTbl.Rows, []string{
			cs.Name,
			cs.Image,
			strconv.FormatBool(cs.Ready),
			strconv.Itoa(int(cs.RestartCount)),
			state,
		})
	}

	// Recent events for this pod.
	evtOpts := metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s,involvedObject.kind=Pod", a.Name, a.Namespace),
		Limit:         10,
	}
	events, _ := client.Events(a.Namespace, evtOpts)

	var evtLines []string
	if events != nil {
		for _, e := range events.Items {
			evtLines = append(evtLines, fmt.Sprintf("[%s] %s: %s", e.Type, e.Reason, e.Message))
		}
	}

	var sb strings.Builder
	if t.hub.MultiContext() {
		ctxName := a.Context
		if ctxName == "" {
			ctxName = t.hub.Default()
		}
		fmt.Fprintf(&sb, "Context: %s\n", ctxName)
	}
	fmt.Fprintf(&sb, "Pod: %s/%s\n", pod.Namespace, pod.Name)
	fmt.Fprintf(&sb, "Phase: %s | IP: %s | Node: %s\n", pod.Status.Phase, pod.Status.PodIP, pod.Spec.NodeName)
	if len(evtLines) > 0 {
		fmt.Fprintf(&sb, "Events:\n  %s\n", strings.Join(evtLines, "\n  "))
	}

	return tools.Observation{Text: sb.String(), Table: cTbl, Raw: pod}, nil
}

func containerState(s corev1.ContainerState) string {
	switch {
	case s.Running != nil:
		return fmt.Sprintf("Running since %s", s.Running.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	case s.Waiting != nil:
		return fmt.Sprintf("Waiting: %s", s.Waiting.Reason)
	case s.Terminated != nil:
		return fmt.Sprintf("Terminated: %s (exit %d)", s.Terminated.Reason, s.Terminated.ExitCode)
	default:
		return "Unknown"
	}
}
