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

// NewListStatefulSetsTool returns the k8s.list_statefulsets tool.
func NewListStatefulSetsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[appsv1.StatefulSet]{
		Name:        "k8s.list_statefulsets",
		Description: "List Kubernetes StatefulSets (apps/v1) in a namespace with ready/desired replicas and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list stateful sets in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of stateful sets to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "READY", "AVAILABLE", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]appsv1.StatefulSet, any, error) {
			list, err := client.StatefulSets(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(s appsv1.StatefulSet) []string {
			desired := int32(0)
			if s.Spec.Replicas != nil {
				desired = *s.Spec.Replicas
			}
			age := ""
			if !s.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(s.CreationTimestamp.Time))
			}
			return []string{
				s.Namespace, s.Name,
				fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, desired),
				fmt.Sprintf("%d", s.Status.AvailableReplicas),
				age,
			}
		},
		Summary: func(items []appsv1.StatefulSet, a listArgs) string {
			return fmt.Sprintf("%d statefulset(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
