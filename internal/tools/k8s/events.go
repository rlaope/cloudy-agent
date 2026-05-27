package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

type eventsArgs struct {
	Namespace          string `json:"namespace"`
	InvolvedObjectKind string `json:"involved_object_kind"`
	InvolvedObjectName string `json:"involved_object_name"`
	Limit              int64  `json:"limit"`
	Context            string `json:"context"`
}

// NewEventsTool returns the k8s.events tool. Events do not fit ListResourceSpec
// because they take involved-object selectors instead of generic label/field
// selectors, so this tool uses Spec[eventsArgs] directly.
func NewEventsTool(hub *k8sclient.Hub) tools.Tool {
	return tools.Spec[eventsArgs]{
		Name:        "k8s.events",
		Description: "List Kubernetes events in a namespace, optionally filtered by involved object kind/name. Results are sorted by lastTimestamp descending.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":            strProp("Namespace to list events in."),
			"involved_object_kind": strProp("Filter by kind of the involved object, e.g. Pod."),
			"involved_object_name": strProp("Filter by name of the involved object."),
			"limit":                intProp("Maximum number of events to return (0 = server default)."),
		}), nil),
		Run: func(_ context.Context, a eventsArgs) (tools.Observation, error) {
			if err := hub.CheckNamespace(a.Namespace); err != nil {
				return tools.Observation{Text: err.Error()}, nil
			}
			client, err := hub.Get(a.Context)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.events: %w", err)
			}
			ctxName := a.Context
			if ctxName == "" {
				ctxName = hub.Default()
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
			eventList, err := client.Events(a.Namespace, opts)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.events: %w", err)
			}

			items := eventList.Items
			sort.Slice(items, func(i, j int) bool {
				return items[i].LastTimestamp.After(items[j].LastTimestamp.Time)
			})

			multi := hub.MultiContext()
			headers := []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"}
			if multi {
				headers = append([]string{"CONTEXT"}, headers...)
			}
			tbl := &render.Table{Headers: headers}
			for _, e := range items {
				age := ""
				if !e.LastTimestamp.IsZero() {
					age = formatAge(time.Since(e.LastTimestamp.Time))
				}
				obj := fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name)
				row := []string{age, e.Type, e.Reason, obj, e.Message}
				if multi {
					row = append([]string{ctxName}, row...)
				}
				tbl.Rows = append(tbl.Rows, row)
			}

			return tools.Observation{
				Text:  fmt.Sprintf("%d event(s) in namespace %q", len(items), a.Namespace),
				Table: tbl,
				Raw:   eventList,
			}, nil
		},
	}.Build()
}
