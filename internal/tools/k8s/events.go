package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// EventsTool implements k8s.events.
type EventsTool struct{ client *Client }

// NewEventsTool constructs an EventsTool backed by the given Client.
func NewEventsTool(c *Client) *EventsTool { return &EventsTool{client: c} }

func (t *EventsTool) Name() string      { return "k8s.events" }
func (t *EventsTool) ReadOnly() bool    { return true }
func (t *EventsTool) Description() string {
	return "List Kubernetes events in a namespace, optionally filtered by involved object kind/name. Results are sorted by lastTimestamp descending."
}
func (t *EventsTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"namespace":            strProp("Namespace to list events in."),
		"involved_object_kind": strProp("Filter by kind of the involved object, e.g. Pod."),
		"involved_object_name": strProp("Filter by name of the involved object."),
		"limit":                intProp("Maximum number of events to return (0 = server default)."),
	}, nil)
}

func (t *EventsTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace           string `json:"namespace"`
		InvolvedObjectKind  string `json:"involved_object_kind"`
		InvolvedObjectName  string `json:"involved_object_name"`
		Limit               int64  `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.events: parse args: %w", err)
	}

	var selectors []string
	if a.InvolvedObjectKind != "" {
		selectors = append(selectors, fmt.Sprintf("involvedObject.kind=%s", a.InvolvedObjectKind))
	}
	if a.InvolvedObjectName != "" {
		selectors = append(selectors, fmt.Sprintf("involvedObject.name=%s", a.InvolvedObjectName))
	}

	fieldSel := ""
	for i, s := range selectors {
		if i > 0 {
			fieldSel += ","
		}
		fieldSel += s
	}

	opts := metav1.ListOptions{FieldSelector: fieldSel}
	if a.Limit > 0 {
		opts.Limit = a.Limit
	}

	evts, err := t.client.Events(a.Namespace, opts)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.events: %w", err)
	}

	// Sort by lastTimestamp descending.
	items := evts.Items
	sort.Slice(items, func(i, j int) bool {
		ti := items[i].LastTimestamp.Time
		tj := items[j].LastTimestamp.Time
		return ti.After(tj)
	})

	tbl := &render.Table{
		Headers: []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"},
	}
	for _, e := range items {
		age := ""
		if !e.LastTimestamp.IsZero() {
			age = formatAge(time.Since(e.LastTimestamp.Time))
		}
		obj := fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name)
		tbl.Rows = append(tbl.Rows, []string{age, e.Type, e.Reason, obj, e.Message})
	}

	text := fmt.Sprintf("%d event(s) in namespace %q", len(items), a.Namespace)
	return tools.Observation{Text: text, Table: tbl, Raw: evts}, nil
}
