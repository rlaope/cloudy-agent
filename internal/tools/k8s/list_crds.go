package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apischema "k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// crdGVR is the GroupVersionResource for installed CRDs themselves. We read
// them through the dynamic client (rather than depending on the
// apiextensions-apiserver typed clientset) so cloudy does not pull in a
// second k8s.io module just to enumerate one cluster-scoped resource.
var crdGVR = apischema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

type listCRDsArgs struct {
	Context      string `json:"context"`
	Group        string `json:"group"`
	NameContains string `json:"name_contains"`
	Limit        int64  `json:"limit"`
}

// NewListCRDsTool returns the k8s.list_crds tool. CRDs do not fit
// ListResourceSpec[T] because the underlying read is an unstructured list,
// not a typed clientset call, so this tool uses Spec[listCRDsArgs] directly.
func NewListCRDsTool(hub *Hub) tools.Tool {
	return tools.Spec[listCRDsArgs]{
		Name:        "k8s.list_crds",
		Description: "List installed CustomResourceDefinitions. Use this to discover which CRD-defined platform extensions (Argo Rollouts, KEDA, cert-manager, Gateway API, ServiceMonitor, etc.) are installed before driving k8s.list_cr against them.",
		Schema: schema(withContextProp(map[string]any{
			"group":         strProp("Filter by API group (exact match), e.g. argoproj.io."),
			"name_contains": strProp("Substring filter against CRD name, e.g. rollout."),
			"limit":         intProp("Maximum number of CRDs to return (default 50)."),
		}), nil),
		Run: func(ctx context.Context, a listCRDsArgs) (tools.Observation, error) {
			client, err := hub.Get(a.Context)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.list_crds: %w", err)
			}
			dyn := client.Dyn()
			if dyn == nil {
				return tools.Observation{Text: "k8s.list_crds: dynamic client unavailable for this context"}, nil
			}
			ctxName := a.Context
			if ctxName == "" {
				ctxName = hub.Default()
			}

			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}

			raw, err := dyn.Resource(crdGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return tools.Observation{}, fmt.Errorf("k8s.list_crds: %w", err)
			}

			items := raw.Items
			// Stable alphabetical ordering by name — list_crds is a discovery
			// surface and inconsistent ordering between calls is hostile to
			// LLM caching and to human eyeballing.
			sort.Slice(items, func(i, j int) bool {
				return items[i].GetName() < items[j].GetName()
			})

			multi := hub.MultiContext()
			headers := []string{"NAME", "GROUP", "VERSIONS", "SCOPE", "KIND", "PLURAL", "ESTABLISHED"}
			aligns := []render.Align{
				render.AlignLeft, render.AlignLeft, render.AlignLeft,
				render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft,
			}
			if multi {
				headers = append([]string{"CONTEXT"}, headers...)
				aligns = append([]render.Align{render.AlignLeft}, aligns...)
			}
			tbl := &render.Table{Headers: headers, Aligns: aligns}

			matched := 0
			for i := range items {
				it := &items[i]
				name := it.GetName()
				group, _, _ := unstructuredString(it.Object, "spec", "group")

				if a.Group != "" && group != a.Group {
					continue
				}
				if a.NameContains != "" && !strings.Contains(name, a.NameContains) {
					continue
				}

				versions := crdVersions(it.Object)
				scope, _, _ := unstructuredString(it.Object, "spec", "scope")
				kind, _, _ := unstructuredString(it.Object, "spec", "names", "kind")
				plural, _, _ := unstructuredString(it.Object, "spec", "names", "plural")
				established := crdEstablishedStatus(it.Object)

				row := []string{name, group, versions, scope, kind, plural, established}
				if multi {
					row = append([]string{ctxName}, row...)
				}
				tbl.Rows = append(tbl.Rows, row)

				matched++
				if int64(matched) >= limit {
					break
				}
			}

			text := fmt.Sprintf("%d CRD(s) shown", matched)
			if a.Group != "" || a.NameContains != "" {
				text += " (filtered)"
			}
			return tools.Observation{Text: text, Table: tbl, Raw: raw}, nil
		},
	}.Build()
}

// crdVersions returns a compact "v1,v1beta1" listing pulled from spec.versions.
func crdVersions(obj map[string]any) string {
	versions, ok, _ := unstructuredSlice(obj, "spec", "versions")
	if !ok {
		return ""
	}
	names := make([]string, 0, len(versions))
	for _, v := range versions {
		vmap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if name, ok, _ := unstructuredString(vmap, "name"); ok {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

// crdEstablishedStatus returns "True", "False", or "" for the Established
// condition on a CRD's status, mirroring `kubectl get crd` semantics.
func crdEstablishedStatus(obj map[string]any) string {
	conds, ok, _ := unstructuredSlice(obj, "status", "conditions")
	if !ok {
		return ""
	}
	for _, c := range conds {
		cmap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _, _ := unstructuredString(cmap, "type")
		if t != "Established" {
			continue
		}
		s, _, _ := unstructuredString(cmap, "status")
		return s
	}
	return ""
}
