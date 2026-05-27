package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

type describePodArgs struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Context   string `json:"context"`
}

// NewDescribePodTool returns the k8s.describe_pod tool.
func NewDescribePodTool(hub *k8sclient.Hub) tools.Tool {
	return tools.Spec[describePodArgs]{
		Name:        "k8s.describe_pod",
		Description: "Return a structured summary of a pod: phase, containers, restart counts, recent events, IP, node.",
		Schema: schema(withContextProp(map[string]any{
			"namespace": strProp("Namespace of the pod."),
			"name":      strProp("Name of the pod."),
		}), []string{"namespace", "name"}),
		Run: func(_ context.Context, a describePodArgs) (tools.Observation, error) {
			if a.Name == "" {
				return tools.Observation{}, fmt.Errorf("k8s.describe_pod: name is required")
			}
			if err := hub.CheckNamespace(a.Namespace); err != nil {
				return tools.Observation{Text: err.Error()}, nil
			}
			client, err := hub.Get(a.Context)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.describe_pod: %w", err)
			}
			pod, err := client.Pod(a.Namespace, a.Name)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.describe_pod: %w", err)
			}

			cTbl := &render.Table{Headers: []string{"CONTAINER", "IMAGE", "READY", "RESTARTS", "STATE"}}
			for _, cs := range pod.Status.ContainerStatuses {
				cTbl.Rows = append(cTbl.Rows, []string{
					cs.Name, cs.Image,
					strconv.FormatBool(cs.Ready),
					strconv.Itoa(int(cs.RestartCount)),
					containerState(cs.State),
				})
			}

			evtOpts := metav1.ListOptions{
				FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s,involvedObject.kind=Pod",
					a.Name, a.Namespace),
				Limit: 10,
			}
			events, _ := client.Events(a.Namespace, evtOpts)

			var evtLines []string
			if events != nil {
				for _, e := range events.Items {
					evtLines = append(evtLines, fmt.Sprintf("[%s] %s: %s", e.Type, e.Reason, e.Message))
				}
			}

			var sb strings.Builder
			if hub.MultiContext() {
				ctxName := a.Context
				if ctxName == "" {
					ctxName = hub.Default()
				}
				fmt.Fprintf(&sb, "Context: %s\n", ctxName)
			}
			fmt.Fprintf(&sb, "Pod: %s/%s\n", pod.Namespace, pod.Name)
			fmt.Fprintf(&sb, "Phase: %s | IP: %s | Node: %s\n", pod.Status.Phase, pod.Status.PodIP, pod.Spec.NodeName)
			if len(evtLines) > 0 {
				fmt.Fprintf(&sb, "Events:\n  %s\n", strings.Join(evtLines, "\n  "))
			}

			return tools.Observation{Text: sb.String(), Table: cTbl, Raw: pod}, nil
		},
	}.Build()
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
