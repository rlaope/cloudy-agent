package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListPDBsTool returns the k8s.list_pdbs tool.
func NewListPDBsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[policyv1.PodDisruptionBudget]{
		Name:        "k8s.list_pdbs",
		Description: "List PodDisruptionBudgets (policy/v1) in a namespace with min-available, max-unavailable, disrupted-pods-allowed, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list PDBs in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of PDBs to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "MIN AVAILABLE", "MAX UNAVAILABLE", "ALLOWED DISRUPTIONS", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]policyv1.PodDisruptionBudget, any, error) {
			list, err := client.PDBs(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(p policyv1.PodDisruptionBudget) []string {
			minA := "<none>"
			if p.Spec.MinAvailable != nil {
				minA = p.Spec.MinAvailable.String()
			}
			maxU := "<none>"
			if p.Spec.MaxUnavailable != nil {
				maxU = p.Spec.MaxUnavailable.String()
			}
			age := ""
			if !p.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(p.CreationTimestamp.Time))
			}
			return []string{
				p.Namespace, p.Name, minA, maxU,
				fmt.Sprintf("%d", p.Status.DisruptionsAllowed),
				age,
			}
		},
		Summary: func(items []policyv1.PodDisruptionBudget, a listArgs) string {
			return fmt.Sprintf("%d PDB(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
