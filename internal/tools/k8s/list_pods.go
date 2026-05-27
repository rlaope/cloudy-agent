package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListPodsTool returns the k8s.list_pods tool.
func NewListPodsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[corev1.Pod]{
		Name:        "k8s.list_pods",
		Description: "List Kubernetes pods in a namespace with optional label/field selectors.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list pods in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector, e.g. app=nginx."),
			"field_selector": strProp("Field selector, e.g. status.phase=Running."),
			"limit":          intProp("Maximum number of pods to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "PHASE", "READY", "RESTARTS", "AGE", "NODE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignLeft},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]corev1.Pod, any, error) {
			list, err := client.Pods(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(p corev1.Pod) []string {
			ready, total, restarts := 0, len(p.Spec.Containers), int32(0)
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
				restarts += cs.RestartCount
			}
			age := ""
			if !p.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(p.CreationTimestamp.Time))
			}
			return []string{
				p.Namespace, p.Name, string(p.Status.Phase),
				fmt.Sprintf("%d/%d", ready, total),
				strconv.Itoa(int(restarts)),
				age, p.Spec.NodeName,
			}
		},
		Summary: func(items []corev1.Pod, a listArgs) string {
			return fmt.Sprintf("%d pod(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
