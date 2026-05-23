package k8s

import (
	"context"
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListHPATool returns the k8s.list_hpa tool.
func NewListHPATool(hub *Hub) tools.Tool {
	return ListResourceSpec[autoscalingv2.HorizontalPodAutoscaler]{
		Name:        "k8s.list_hpa",
		Description: "List HorizontalPodAutoscalers (autoscaling/v2) in a namespace with target ref, replica bounds, current replicas, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list HPAs in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of HPAs to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "REFERENCE", "MIN", "MAX", "REPLICAS", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *Client, a listArgs, opts metav1.ListOptions) ([]autoscalingv2.HorizontalPodAutoscaler, any, error) {
			list, err := client.HPAs(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(h autoscalingv2.HorizontalPodAutoscaler) []string {
			ref := fmt.Sprintf("%s/%s", h.Spec.ScaleTargetRef.Kind, h.Spec.ScaleTargetRef.Name)
			minR := int32(0)
			if h.Spec.MinReplicas != nil {
				minR = *h.Spec.MinReplicas
			}
			age := ""
			if !h.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(h.CreationTimestamp.Time))
			}
			return []string{
				h.Namespace, h.Name, ref,
				fmt.Sprintf("%d", minR),
				fmt.Sprintf("%d", h.Spec.MaxReplicas),
				fmt.Sprintf("%d", h.Status.CurrentReplicas),
				age,
			}
		},
		Summary: func(items []autoscalingv2.HorizontalPodAutoscaler, a listArgs) string {
			return fmt.Sprintf("%d HPA(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
