package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// NewListDaemonSetsTool returns the k8s.list_daemonsets tool.
func NewListDaemonSetsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[appsv1.DaemonSet]{
		Name:        "k8s.list_daemonsets",
		Description: "List Kubernetes DaemonSets (apps/v1) in a namespace with desired/current/ready counts and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list daemon sets in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of daemon sets to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "DESIRED", "CURRENT", "READY", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]appsv1.DaemonSet, any, error) {
			list, err := client.DaemonSets(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(d appsv1.DaemonSet) []string {
			age := ""
			if !d.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(d.CreationTimestamp.Time))
			}
			return []string{
				d.Namespace, d.Name,
				fmt.Sprintf("%d", d.Status.DesiredNumberScheduled),
				fmt.Sprintf("%d", d.Status.CurrentNumberScheduled),
				fmt.Sprintf("%d", d.Status.NumberReady),
				age,
			}
		},
		Summary: func(items []appsv1.DaemonSet, a listArgs) string {
			return fmt.Sprintf("%d daemonset(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
