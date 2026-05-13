package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/tools"
)

// NewListNodesTool returns the k8s.list_nodes tool.
func NewListNodesTool(hub *Hub) tools.Tool {
	return ListResourceSpec[corev1.Node]{
		Name:        "k8s.list_nodes",
		Description: "List all cluster nodes with name, roles, Kubernetes version, status, age, and addresses.",
		Schema:      schema(withContextProp(map[string]any{}), nil),
		Headers:     []string{"NAME", "ROLES", "VERSION", "STATUS", "AGE", "ADDRESSES"},
		Items: func(_ context.Context, client *Client, _ listArgs, _ metav1.ListOptions) ([]corev1.Node, any, error) {
			list, err := client.Nodes()
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(n corev1.Node) []string {
			age := ""
			if !n.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(n.CreationTimestamp.Time))
			}
			return []string{
				n.Name, nodeRoles(n), n.Status.NodeInfo.KubeletVersion,
				nodeStatus(n), age, nodeAddresses(n),
			}
		},
		Summary: func(items []corev1.Node, _ listArgs) string {
			return fmt.Sprintf("%d node(s)", len(items))
		},
	}.Build(hub)
}

func nodeRoles(n corev1.Node) string {
	var roles []string
	for k := range n.Labels {
		if strings.HasPrefix(k, "node-role.kubernetes.io/") {
			role := strings.TrimPrefix(k, "node-role.kubernetes.io/")
			if role != "" {
				roles = append(roles, role)
			}
		}
	}
	if len(roles) == 0 {
		return "<none>"
	}
	return strings.Join(roles, ",")
}

func nodeStatus(n corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func nodeAddresses(n corev1.Node) string {
	var addrs []string
	for _, a := range n.Status.Addresses {
		addrs = append(addrs, a.Address)
	}
	return strings.Join(addrs, ",")
}
