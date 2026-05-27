package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListNetworkPoliciesTool returns the k8s.list_networkpolicies tool.
func NewListNetworkPoliciesTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[networkingv1.NetworkPolicy]{
		Name:        "k8s.list_networkpolicies",
		Description: "List NetworkPolicies (networking.k8s.io/v1) in a namespace with the pod selector and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list network policies in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of network policies to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "POD-SELECTOR", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]networkingv1.NetworkPolicy, any, error) {
			list, err := client.NetworkPolicies(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(p networkingv1.NetworkPolicy) []string {
			age := ""
			if !p.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(p.CreationTimestamp.Time))
			}
			return []string{
				p.Namespace, p.Name,
				podSelector(p.Spec.PodSelector),
				age,
			}
		},
		Summary: func(items []networkingv1.NetworkPolicy, a listArgs) string {
			return fmt.Sprintf("%d networkpolicy(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

// podSelector renders a LabelSelector compactly. An empty selector matches all
// pods in the namespace, which kubectl prints as "<none>".
func podSelector(sel metav1.LabelSelector) string {
	if len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0 {
		return "<none>"
	}
	keys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+sel.MatchLabels[k])
	}
	return strings.Join(parts, ",")
}
