package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// NewListIngressesTool returns the k8s.list_ingresses tool.
func NewListIngressesTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[networkingv1.Ingress]{
		Name:        "k8s.list_ingresses",
		Description: "List Kubernetes Ingresses (networking.k8s.io/v1) in a namespace with hosts, load-balancer address, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list ingresses in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of ingresses to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "HOSTS", "ADDRESS", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]networkingv1.Ingress, any, error) {
			list, err := client.Ingresses(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(i networkingv1.Ingress) []string {
			age := ""
			if !i.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(i.CreationTimestamp.Time))
			}
			return []string{
				i.Namespace, i.Name,
				ingressHosts(i), ingressAddress(i), age,
			}
		},
		Summary: func(items []networkingv1.Ingress, a listArgs) string {
			return fmt.Sprintf("%d ingress(es) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

func ingressHosts(i networkingv1.Ingress) string {
	if len(i.Spec.Rules) == 0 {
		return "<none>"
	}
	hosts := make([]string, 0, len(i.Spec.Rules))
	for _, r := range i.Spec.Rules {
		if r.Host != "" {
			hosts = append(hosts, r.Host)
		}
	}
	if len(hosts) == 0 {
		return "*"
	}
	return strings.Join(hosts, ",")
}

func ingressAddress(i networkingv1.Ingress) string {
	addrs := make([]string, 0, len(i.Status.LoadBalancer.Ingress))
	for _, lb := range i.Status.LoadBalancer.Ingress {
		switch {
		case lb.Hostname != "":
			addrs = append(addrs, lb.Hostname)
		case lb.IP != "":
			addrs = append(addrs, lb.IP)
		}
	}
	if len(addrs) == 0 {
		return "<pending>"
	}
	return strings.Join(addrs, ",")
}
