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
type ListNamespacesTool struct{ hub *Hub }

// NewListNamespacesTool constructs a ListNamespacesTool backed by the given Hub.
func NewListNamespacesTool(h *Hub) *ListNamespacesTool { return &ListNamespacesTool{hub: h} }

func (t *ListNamespacesTool) Name() string   { return "k8s.list_namespaces" }
func (t *ListNamespacesTool) ReadOnly() bool { return true }
func (t *ListNamespacesTool) Description() string {
	return "List all Kubernetes namespaces in the cluster."
}
func (t *ListNamespacesTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{}), nil)
}

func (t *ListNamespacesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Context string `json:"context"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("k8s.list_namespaces: parse args: %w", err)
		}
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_namespaces: %w", err)
	}
	ctxName := a.Context
	if ctxName == "" {
		ctxName = t.hub.Default()
	}

	nsList, err := client.Namespaces()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_namespaces: %w", err)
	}

	multi := t.hub.MultiContext()
	headers := []string{"NAME", "STATUS", "AGE"}
	if multi {
		headers = append([]string{"CONTEXT"}, headers...)
	}
	tbl := &render.Table{Headers: headers}
	for _, ns := range nsList.Items {
		age := ""
		if !ns.CreationTimestamp.IsZero() {
			age = formatAge(time.Since(ns.CreationTimestamp.Time))
		}
		row := []string{
			ns.Name,
			string(ns.Status.Phase),
			age,
		}
		if multi {
			row = append([]string{ctxName}, row...)
		}
		tbl.Rows = append(tbl.Rows, row)
	}

	return tools.Observation{
		Text:  fmt.Sprintf("%d namespace(s)", len(nsList.Items)),
		Table: tbl,
		Raw:   nsList,
	}, nil
}
