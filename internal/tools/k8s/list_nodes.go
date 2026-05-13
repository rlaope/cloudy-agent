package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// ListNodesTool implements k8s.list_nodes.
type ListNodesTool struct{ client *Client }

// NewListNodesTool constructs a ListNodesTool backed by the given Client.
func NewListNodesTool(c *Client) *ListNodesTool { return &ListNodesTool{client: c} }

func (t *ListNodesTool) Name() string      { return "k8s.list_nodes" }
func (t *ListNodesTool) ReadOnly() bool    { return true }
func (t *ListNodesTool) Description() string {
	return "List all cluster nodes with name, roles, Kubernetes version, status, age, and addresses."
}
func (t *ListNodesTool) Schema() json.RawMessage {
	return schema(map[string]any{}, nil)
}

func (t *ListNodesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	nodeList, err := t.client.Nodes()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_nodes: %w", err)
	}

	tbl := &render.Table{
		Headers: []string{"NAME", "ROLES", "VERSION", "STATUS", "AGE", "ADDRESSES"},
	}
	for _, n := range nodeList.Items {
		roles := nodeRoles(n)
		status := nodeStatus(n)
		age := ""
		if !n.CreationTimestamp.IsZero() {
			age = formatAge(time.Since(n.CreationTimestamp.Time))
		}
		addrs := nodeAddresses(n)
		tbl.Rows = append(tbl.Rows, []string{
			n.Name,
			roles,
			n.Status.NodeInfo.KubeletVersion,
			status,
			age,
			addrs,
		})
	}

	return tools.Observation{
		Text:  fmt.Sprintf("%d node(s)", len(nodeList.Items)),
		Table: tbl,
		Raw:   nodeList,
	}, nil
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
