package k8s

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListServicesTool returns the k8s.list_services tool.
func NewListServicesTool(hub *Hub) tools.Tool {
	return ListResourceSpec[corev1.Service]{
		Name:        "k8s.list_services",
		Description: "List Kubernetes Services (core/v1) in a namespace with type, cluster IP, ports, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list services in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of services to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "TYPE", "CLUSTER-IP", "PORTS", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *Client, a listArgs, opts metav1.ListOptions) ([]corev1.Service, any, error) {
			list, err := client.ServicesList(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(s corev1.Service) []string {
			clusterIP := s.Spec.ClusterIP
			if clusterIP == "" {
				clusterIP = "<none>"
			}
			age := ""
			if !s.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(s.CreationTimestamp.Time))
			}
			return []string{
				s.Namespace, s.Name, string(s.Spec.Type),
				clusterIP, servicePorts(s), age,
			}
		},
		Summary: func(items []corev1.Service, a listArgs) string {
			return fmt.Sprintf("%d service(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

func servicePorts(s corev1.Service) string {
	if len(s.Spec.Ports) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(s.Spec.Ports))
	for _, p := range s.Spec.Ports {
		proto := string(p.Protocol)
		if proto == "" {
			proto = "TCP"
		}
		parts = append(parts, strconv.Itoa(int(p.Port))+"/"+proto)
	}
	return strings.Join(parts, ",")
}
