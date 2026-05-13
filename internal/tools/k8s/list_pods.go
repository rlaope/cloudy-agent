package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// ListPodsTool implements k8s.list_pods.
type ListPodsTool struct{ client *Client }

// NewListPodsTool constructs a ListPodsTool backed by the given Client.
func NewListPodsTool(c *Client) *ListPodsTool { return &ListPodsTool{client: c} }

func (t *ListPodsTool) Name() string      { return "k8s.list_pods" }
func (t *ListPodsTool) ReadOnly() bool    { return true }
func (t *ListPodsTool) Description() string {
	return "List Kubernetes pods in a namespace with optional label/field selectors."
}
func (t *ListPodsTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"namespace":      strProp("Namespace to list pods in. Empty string means all namespaces."),
		"label_selector": strProp("Label selector, e.g. app=nginx."),
		"field_selector": strProp("Field selector, e.g. status.phase=Running."),
		"limit":          intProp("Maximum number of pods to return (0 = server default)."),
	}, nil)
}

func (t *ListPodsTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace     string `json:"namespace"`
		LabelSelector string `json:"label_selector"`
		FieldSelector string `json:"field_selector"`
		Limit         int64  `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_pods: parse args: %w", err)
	}

	opts := metav1.ListOptions{
		LabelSelector: a.LabelSelector,
		FieldSelector: a.FieldSelector,
	}
	if a.Limit > 0 {
		opts.Limit = a.Limit
	}

	pods, err := t.client.Pods(a.Namespace, opts)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.list_pods: %w", err)
	}

	tbl := &render.Table{
		Headers: []string{"NAMESPACE", "NAME", "PHASE", "READY", "RESTARTS", "AGE", "NODE"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignLeft},
	}
	for _, p := range pods.Items {
		ready := 0
		total := len(p.Spec.Containers)
		restarts := int32(0)
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}
		age := ""
		if !p.CreationTimestamp.IsZero() {
			age = formatAge(time.Since(p.CreationTimestamp.Time))
		}
		tbl.Rows = append(tbl.Rows, []string{
			p.Namespace,
			p.Name,
			string(p.Status.Phase),
			fmt.Sprintf("%d/%d", ready, total),
			strconv.Itoa(int(restarts)),
			age,
			p.Spec.NodeName,
		})
	}

	text := fmt.Sprintf("%d pod(s) in namespace %q", len(pods.Items), a.Namespace)
	return tools.Observation{Text: text, Table: tbl, Raw: pods}, nil
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
