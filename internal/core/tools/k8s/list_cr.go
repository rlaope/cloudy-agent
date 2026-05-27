package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apischema "k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// defaultCRFields is the projection used when the caller does not pass an
// explicit fields argument. It matches the columns SREs eyeball first on a
// generic "what custom resources exist?" question and works against the
// overwhelming majority of CRDs (Argo Rollouts, KEDA ScaledObject,
// ServiceMonitor, cert-manager Certificate all expose status.phase or a
// similar top-level discriminator).
var defaultCRFields = []string{"metadata.name", "metadata.namespace", "status.phase"}

type listCRArgs struct {
	Context       string   `json:"context"`
	Group         string   `json:"group"`
	Version       string   `json:"version"`
	Resource      string   `json:"resource"`
	Namespace     string   `json:"namespace"`
	LabelSelector string   `json:"label_selector"`
	Limit         int64    `json:"limit"`
	Fields        []string `json:"fields"`
}

// NewListCRTool returns the k8s.list_cr tool. It is the generic counterpart
// to the typed list_* tools: instead of hard-coding a GVR per resource, the
// caller passes group/version/resource and (optionally) a list of dotted
// JSONPath-style field projections. Per the spec, only the dotted-path 95%
// case is supported — array indexing (status.conditions[0].status) is out
// of scope.
func NewListCRTool(hub *k8sclient.Hub) tools.Tool {
	return tools.Spec[listCRArgs]{
		Name:        "k8s.list_cr",
		Description: "List custom resources of the given group/version/resource using the dynamic client. Use k8s.list_crds first to discover which GVRs exist. Fields are projected via dotted JSONPath-style lookups (e.g. metadata.name, spec.replicas).",
		Schema: schema(withContextProp(map[string]any{
			"group":          strProp("API group, e.g. argoproj.io. Required."),
			"version":        strProp("API version, e.g. v1alpha1. Required."),
			"resource":       strProp("Plural resource name, e.g. rollouts. Required."),
			"namespace":      strProp("Namespace to list in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector, e.g. app=checkout."),
			"limit":          intProp("Maximum number of items to return (default 50)."),
			"fields": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Dotted field paths to project as table columns, e.g. ['metadata.name','spec.replicas','status.phase']. Defaults to [metadata.name, metadata.namespace, status.phase].",
			},
		}), []string{"group", "version", "resource"}),
		Run: func(ctx context.Context, a listCRArgs) (tools.Observation, error) {
			if a.Group == "" || a.Version == "" || a.Resource == "" {
				return tools.Observation{}, fmt.Errorf("k8s.list_cr: group, version, resource are required")
			}
			if err := hub.CheckNamespace(a.Namespace); err != nil {
				return tools.Observation{Text: err.Error()}, nil
			}
			client, err := hub.Get(a.Context)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.list_cr: %w", err)
			}
			dyn := client.Dyn()
			if dyn == nil {
				return tools.Observation{Text: "k8s.list_cr: dynamic client unavailable for this context"}, nil
			}
			ctxName := a.Context
			if ctxName == "" {
				ctxName = hub.Default()
			}

			fields := a.Fields
			if len(fields) == 0 {
				fields = defaultCRFields
			}

			gvr := apischema.GroupVersionResource{
				Group:    a.Group,
				Version:  a.Version,
				Resource: a.Resource,
			}

			opts := metav1.ListOptions{LabelSelector: a.LabelSelector}
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			opts.Limit = limit

			var raw any
			var items []map[string]any
			if a.Namespace != "" {
				list, err := dyn.Resource(gvr).Namespace(a.Namespace).List(ctx, opts)
				if err != nil {
					return tools.Observation{}, fmt.Errorf("k8s.list_cr: %w", err)
				}
				raw = list
				for i := range list.Items {
					items = append(items, list.Items[i].Object)
				}
			} else {
				list, err := dyn.Resource(gvr).List(ctx, opts)
				if err != nil {
					return tools.Observation{}, fmt.Errorf("k8s.list_cr: %w", err)
				}
				raw = list
				for i := range list.Items {
					items = append(items, list.Items[i].Object)
				}
			}

			multi := hub.MultiContext()
			headers := make([]string, len(fields))
			aligns := make([]render.Align, len(fields))
			for i, f := range fields {
				headers[i] = strings.ToUpper(f)
				aligns[i] = render.AlignLeft
			}
			if multi {
				headers = append([]string{"CONTEXT"}, headers...)
				aligns = append([]render.Align{render.AlignLeft}, aligns...)
			}
			tbl := &render.Table{Headers: headers, Aligns: aligns}

			for _, obj := range items {
				row := make([]string, len(fields))
				for i, path := range fields {
					row[i] = projectField(obj, path)
				}
				if multi {
					row = append([]string{ctxName}, row...)
				}
				tbl.Rows = append(tbl.Rows, row)
			}

			text := fmt.Sprintf("%d %s/%s/%s item(s)", len(items), a.Group, a.Version, a.Resource)
			if a.Namespace != "" {
				text += fmt.Sprintf(" in namespace %q", a.Namespace)
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}

// projectField resolves a dotted path like "metadata.name" or "spec.replicas"
// against an Unstructured object's map. Returns the empty string when any
// segment is missing — this is the projection contract the LLM relies on:
// missing fields render as blank cells, never errors, because CRDs are
// heterogeneous and a single unknown field on one row should not abort the
// whole table render. Array indexing is intentionally not supported (see
// tool description).
func projectField(obj map[string]any, path string) string {
	if obj == nil || path == "" {
		return ""
	}
	segs := strings.Split(path, ".")
	var cur any = obj
	for _, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		v, ok := m[seg]
		if !ok {
			return ""
		}
		cur = v
	}
	return stringifyScalar(cur)
}

// stringifyScalar renders a JSON-decoded scalar as the cell text. Non-scalar
// values (maps, slices) collapse to "<object>" / "<array>" — projecting an
// object directly is almost always a sign the caller wanted a deeper path
// and rendering the raw map would explode the column width.
func stringifyScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		// JSON numbers decode as float64; render whole numbers without the
		// trailing ".0" so projections like spec.replicas read naturally.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case map[string]any:
		return "<object>"
	case []any:
		return "<array>"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// unstructuredString is a thin nil-safe lookup over an Unstructured object's
// Object map. Returns (value, found, error-on-type-mismatch). Used by
// list_crds.go (siblings on the same package); kept here so the two
// CRD-generic tools share one helper surface.
func unstructuredString(obj map[string]any, path ...string) (string, bool, error) {
	v, ok := walk(obj, path)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", true, fmt.Errorf("k8s: field %v is %T, expected string", path, v)
	}
	return s, true, nil
}

// unstructuredSlice is the slice variant of unstructuredString.
func unstructuredSlice(obj map[string]any, path ...string) ([]any, bool, error) {
	v, ok := walk(obj, path)
	if !ok {
		return nil, false, nil
	}
	s, ok := v.([]any)
	if !ok {
		return nil, true, fmt.Errorf("k8s: field %v is %T, expected []any", path, v)
	}
	return s, true, nil
}

func walk(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}
