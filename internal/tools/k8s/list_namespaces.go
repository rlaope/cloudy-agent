package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// ListNamespacesTool implements k8s.list_namespaces.
type ListNamespacesTool struct{ client *Client }

// NewListNamespacesTool constructs a ListNamespacesTool backed by the given Client.
func NewListNamespacesTool(c *Client) *ListNamespacesTool { return &ListNamespacesTool{client: c} }

func (t *ListNamespacesTool) Name() string      { return "k8s.list_namespaces" }
func (t *ListNamespacesTool) ReadOnly() bool    { return true }
func (t *ListNamespacesTool) Description() string {
	return "List all Kubernetes namespaces in the cluster."
}
func (t *ListNamespacesTool) Schema() json.RawMessage {
	return schema(map[string]any{}, nil)
}

func (t *ListNamespacesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	nsList, err := t.client.Namespaces()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_namespaces: %w", err)
	}

	tbl := &render.Table{
		Headers: []string{"NAME", "STATUS", "AGE"},
	}
	for _, ns := range nsList.Items {
		age := ""
		if !ns.CreationTimestamp.IsZero() {
			age = formatAge(time.Since(ns.CreationTimestamp.Time))
		}
		tbl.Rows = append(tbl.Rows, []string{
			ns.Name,
			string(ns.Status.Phase),
			age,
		})
	}

	return tools.Observation{
		Text:  fmt.Sprintf("%d namespace(s)", len(nsList.Items)),
		Table: tbl,
		Raw:   nsList,
	}, nil
}
