package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListDeploymentsTool returns the k8s.list_deployments tool.
func NewListDeploymentsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[appsv1.Deployment]{
		Name:        "k8s.list_deployments",
		Description: "List Kubernetes Deployments (apps/v1) in a namespace with ready/desired replicas, available status, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list deployments in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector, e.g. app=nginx."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of deployments to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "READY", "AVAILABLE", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignLeft, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]appsv1.Deployment, any, error) {
			list, err := client.Deployments(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(d appsv1.Deployment) []string {
			desired := int32(0)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			age := ""
			if !d.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(d.CreationTimestamp.Time))
			}
			return []string{
				d.Namespace, d.Name,
				fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desired),
				deploymentAvailable(d),
				age,
			}
		},
		Summary: func(items []appsv1.Deployment, a listArgs) string {
			return fmt.Sprintf("%d deployment(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

func deploymentAvailable(d appsv1.Deployment) string {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return string(c.Status)
		}
	}
	return "Unknown"
}
