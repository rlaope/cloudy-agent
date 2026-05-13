package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// listArgs is the common argument shape every list-resource tool accepts.
// Per-resource specs declare which fields they actually honour via the
// schema; unused fields are simply ignored.
type listArgs struct {
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"label_selector"`
	FieldSelector string `json:"field_selector"`
	Limit         int64  `json:"limit"`
	Context       string `json:"context"`
}

// ListResourceSpec describes a single list-shape Kubernetes tool: how to
// fetch its items, how to render one row, and how to label the result. The
// framework owns the cross-cutting concerns shared by every list tool —
// Hub.Get, namespace check, MultiContext CONTEXT column, ListOptions
// assembly, error wrapping, Observation envelope.
type ListResourceSpec[T any] struct {
	// Name is the tool identifier, e.g. "k8s.list_pods".
	Name string
	// Description is shown in the system prompt's tool catalogue.
	Description string
	// Schema is the JSON Schema for this tool's arguments.
	Schema json.RawMessage
	// Headers is the column header list, excluding the optional CONTEXT
	// column which is prepended automatically when Hub is multi-context.
	Headers []string
	// Aligns is the per-column alignment, parallel to Headers.
	Aligns []render.Align
	// NeedsNamespace gates Hub.CheckNamespace before the API call.
	NeedsNamespace bool
	// Items fetches the resource list. Return (items, raw, err); raw becomes
	// Observation.Raw for downstream consumers.
	Items func(ctx context.Context, client *Client, args listArgs, opts metav1.ListOptions) ([]T, any, error)
	// ProjectRow turns one item into a row of strings.
	ProjectRow func(item T) []string
	// Summary returns the one-line text rendered above the table; optional.
	Summary func(items []T, args listArgs) string
}

// Build returns a tools.Tool that drives spec through tools.Spec[listArgs].
func (spec ListResourceSpec[T]) Build(hub *Hub) tools.Tool {
	s := spec
	return tools.Spec[listArgs]{
		Name:        s.Name,
		Description: s.Description,
		Schema:      s.Schema,
		Run: func(ctx context.Context, a listArgs) (tools.Observation, error) {
			if s.NeedsNamespace {
				if err := hub.CheckNamespace(a.Namespace); err != nil {
					return tools.Observation{Text: err.Error()}, nil
				}
			}
			client, err := hub.Get(a.Context)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("%s: %w", s.Name, err)
			}
			ctxName := a.Context
			if ctxName == "" {
				ctxName = hub.Default()
			}

			opts := metav1.ListOptions{
				LabelSelector: a.LabelSelector,
				FieldSelector: a.FieldSelector,
			}
			if a.Limit > 0 {
				opts.Limit = a.Limit
			}
			items, raw, err := s.Items(ctx, client, a, opts)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("%s: %w", s.Name, err)
			}

			multi := hub.MultiContext()
			headers := append([]string(nil), s.Headers...)
			aligns := append([]render.Align(nil), s.Aligns...)
			if multi {
				headers = append([]string{"CONTEXT"}, headers...)
				aligns = append([]render.Align{render.AlignLeft}, aligns...)
			}
			tbl := &render.Table{Headers: headers, Aligns: aligns}
			for _, item := range items {
				row := s.ProjectRow(item)
				if multi {
					row = append([]string{ctxName}, row...)
				}
				tbl.Rows = append(tbl.Rows, row)
			}

			text := ""
			if s.Summary != nil {
				text = s.Summary(items, a)
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}
