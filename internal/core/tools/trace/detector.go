package trace

import (
	"context"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/discovery"
)

func init() { discovery.Register(&detector{}) }

type detector struct{}

func (detector) Name() string { return "tools.trace" }

// listServicesFn is the seam used by tests to replace the real Kubernetes
// service-list call with a fake. Production code uses defaultListServices.
var listServicesFn = defaultListServices

// defaultListServices lists services across all namespaces for a given client.
func defaultListServices(ctx context.Context, client *k8sclient.Client) (*corev1.ServiceList, error) {
	return client.Services(ctx, "", metav1.ListOptions{Limit: 500})
}

func (detector) Detect(ctx context.Context, env discovery.Env) []discovery.Finding {
	var findings []discovery.Finding

	// K8s path: iterate every configured context.
	if env.Hub != nil && env.Proxy != nil {
		for _, ctxName := range env.Hub.Names() {
			client, err := env.Hub.Get(ctxName)
			if err != nil {
				continue
			}

			svcList, err := listServicesFn(ctx, client)
			if err != nil {
				continue
			}

			for _, svc := range svcList.Items {
				ns := svc.Namespace
				name := svc.Name
				nameLower := strings.ToLower(name)
				appLabel := strings.ToLower(svc.Labels["app.kubernetes.io/name"])

				// Tempo heuristics:
				//   name contains "tempo", label app.kubernetes.io/name=tempo,
				//   port 3200 or named "http"/"tempo-prom".
				if tempoNameMatch := strings.Contains(nameLower, "tempo"); tempoNameMatch || appLabel == "tempo" {
					if portStr, ok := tempoPort(svc.Spec.Ports); ok {
						endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
						if endpointURL != "" {
							labelMatch := appLabel == "tempo"
							conf := confidenceScore(tempoNameMatch, labelMatch, true)
							findings = append(findings, discovery.Finding{
								Group: discovery.GroupTrace,
								Kind:  "tempo",
								Source: discovery.Source{
									Context:     ctxName,
									Namespace:   ns,
									ServiceName: name,
								},
								EndpointURL: endpointURL,
								Confidence:  conf,
								AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "tempo"},
								Labels:      map[string]string{},
							})
						}
					}
				} else if portStr, ok := tempoPortExact(svc.Spec.Ports); ok {
					// port-only match (no name/label signal).
					endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
					if endpointURL != "" {
						findings = append(findings, discovery.Finding{
							Group: discovery.GroupTrace,
							Kind:  "tempo",
							Source: discovery.Source{
								Context:     ctxName,
								Namespace:   ns,
								ServiceName: name,
							},
							EndpointURL: endpointURL,
							Confidence:  0.4,
							AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "tempo"},
							Labels:      map[string]string{},
						})
					}
				}

				// Jaeger heuristics:
				//   name contains "jaeger-query" or "jaeger",
				//   label app.kubernetes.io/name=jaeger-query,
				//   port 16686.
				jaegerQueryMatch := strings.Contains(nameLower, "jaeger-query")
				jaegerNameMatch := strings.Contains(nameLower, "jaeger")
				jaegerLabelMatch := appLabel == "jaeger-query"

				if jaegerQueryMatch || jaegerNameMatch || jaegerLabelMatch {
					if portStr, ok := jaegerPort(svc.Spec.Ports); ok {
						endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
						if endpointURL != "" {
							conf := confidenceScore(jaegerQueryMatch || jaegerNameMatch, jaegerLabelMatch, true)
							findings = append(findings, discovery.Finding{
								Group: discovery.GroupTrace,
								Kind:  "jaeger",
								Source: discovery.Source{
									Context:     ctxName,
									Namespace:   ns,
									ServiceName: name,
								},
								EndpointURL: endpointURL,
								Confidence:  conf,
								AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "jaeger"},
								Labels:      map[string]string{},
							})
						}
					}
				} else if portStr, ok := jaegerPortExact(svc.Spec.Ports); ok {
					// port-only match (no name/label signal).
					endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
					if endpointURL != "" {
						findings = append(findings, discovery.Finding{
							Group: discovery.GroupTrace,
							Kind:  "jaeger",
							Source: discovery.Source{
								Context:     ctxName,
								Namespace:   ns,
								ServiceName: name,
							},
							EndpointURL: endpointURL,
							Confidence:  0.4,
							AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "jaeger"},
							Labels:      map[string]string{},
						})
					}
				}
			}
		}
	}

	// Hints path: emit External findings for any hint with Kind tempo or jaeger.
	for _, hint := range env.Hints {
		kind := strings.ToLower(hint.Kind)
		if kind != "tempo" && kind != "jaeger" {
			continue
		}
		if hint.URL == "" {
			continue
		}
		findings = append(findings, discovery.Finding{
			Group: discovery.GroupTrace,
			Kind:  kind,
			Source: discovery.Source{
				External:    true,
				ExternalURL: hint.URL,
			},
			EndpointURL: hint.URL,
			Confidence:  1.0,
			AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: kind},
			Labels:      map[string]string{},
		})
	}

	return findings
}

// tempoPort returns the numeric port string for the first port matching Tempo's
// HTTP port heuristics: port 3200, or named "http" or "tempo-prom".
func tempoPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 3200 || n == "http" || n == "tempo-prom" {
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// tempoPortExact matches only the canonical numeric port 3200 (no name hint).
func tempoPortExact(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		if p.Port == 3200 {
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// jaegerPort returns the numeric port string for the first port matching
// Jaeger query heuristics: port 16686, or named "http" or "query".
func jaegerPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 16686 || n == "http" || n == "query" {
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// jaegerPortExact matches only the canonical numeric port 16686 (no name hint).
func jaegerPortExact(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		if p.Port == 16686 {
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// confidenceScore maps (name, label, port) boolean signals to a 0..1 score.
// All three = 1.0; any two = 0.8; one = 0.5.
func confidenceScore(name, label, port bool) float64 {
	n := 0
	if name {
		n++
	}
	if label {
		n++
	}
	if port {
		n++
	}
	switch n {
	case 3:
		return 1.0
	case 2:
		return 0.8
	default:
		return 0.5
	}
}
