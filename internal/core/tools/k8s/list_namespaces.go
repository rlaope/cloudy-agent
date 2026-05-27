package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// NewListNamespacesTool returns the k8s.list_namespaces tool.
func NewListNamespacesTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[corev1.Namespace]{
		Name:        "k8s.list_namespaces",
		Description: "List all Kubernetes namespaces in the cluster.",
		Schema:      schema(withContextProp(map[string]any{}), nil),
		Headers:     []string{"NAME", "STATUS", "AGE"},
		Items: func(_ context.Context, client *k8sclient.Client, _ listArgs, _ metav1.ListOptions) ([]corev1.Namespace, any, error) {
			list, err := client.Namespaces()
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(ns corev1.Namespace) []string {
			age := ""
			if !ns.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(ns.CreationTimestamp.Time))
			}
			return []string{ns.Name, string(ns.Status.Phase), age}
		},
		Summary: func(items []corev1.Namespace, _ listArgs) string {
			return fmt.Sprintf("%d namespace(s)", len(items))
		},
	}.Build(hub)
}
