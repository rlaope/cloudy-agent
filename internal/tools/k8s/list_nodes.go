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
type ListNodesTool struct{ hub *Hub }

// NewListNodesTool constructs a ListNodesTool backed by the given Hub.
func NewListNodesTool(h *Hub) *ListNodesTool { return &ListNodesTool{hub: h} }

func (t *ListNodesTool) Name() string   { return "k8s.list_nodes" }
func (t *ListNodesTool) ReadOnly() bool { return true }
func (t *ListNodesTool) Description() string {
	return "List all cluster nodes with name, roles, Kubernetes version, status, age, and addresses."
}
func (t *ListNodesTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{}), nil)
}

func (t *ListNodesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Context string `json:"context"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("k8s.list_nodes: parse args: %w", err)
		}
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_nodes: %w", err)
	}
	ctxName := a.Context
	if ctxName == "" {
		ctxName = t.hub.Default()
	}

	nodeList, err := client.Nodes()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_nodes: %w", err)
	}

	multi := t.hub.MultiContext()
	headers := []string{"NAME", "ROLES", "VERSION", "STATUS", "AGE", "ADDRESSES"}
	if multi {
		headers = append([]string{"CONTEXT"}, headers...)
	}
	tbl := &render.Table{Headers: headers}
	for _, n := range nodeList.Items {
		roles := nodeRoles(n)
		status := nodeStatus(n)
		age := ""
		if !n.CreationTimestamp.IsZero() {
			age = formatAge(time.Since(n.CreationTimestamp.Time))
		}
		addrs := nodeAddresses(n)
		row := []string{
			n.Name,
			roles,
			n.Status.NodeInfo.KubeletVersion,
			status,
			age,
			addrs,
		}
		if multi {
			row = append([]string{ctxName}, row...)
		}
		tbl.Rows = append(tbl.Rows, row)
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
