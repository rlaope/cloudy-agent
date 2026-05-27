package prom

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

func (detector) Name() string { return "tools.prom" }

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

				nameMatch := strings.Contains(nameLower, "prometheus")
				labelMatch := appLabel == "prometheus"

				if !nameMatch && !labelMatch {
					// Check for port 9090 only match.
					portStr, ok := promPort(svc.Spec.Ports)
					if !ok {
						continue
					}
					endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
					if endpointURL == "" {
						continue
					}
					findings = append(findings, discovery.Finding{
						Group: discovery.GroupProm,
						Kind:  "prometheus",
						Source: discovery.Source{
							Context:     ctxName,
							Namespace:   ns,
							ServiceName: name,
						},
						EndpointURL: endpointURL,
						Confidence:  0.4,
						AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "prometheus"},
						Labels:      map[string]string{},
					})
					continue
				}

				portStr, ok := promPort(svc.Spec.Ports)
				if !ok {
					continue
				}
				endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
				if endpointURL == "" {
					continue
				}

				var confidence float64
				switch {
				case nameMatch && labelMatch:
					confidence = 1.0
				default:
					confidence = 0.7
				}

				findings = append(findings, discovery.Finding{
					Group: discovery.GroupProm,
					Kind:  "prometheus",
					Source: discovery.Source{
						Context:     ctxName,
						Namespace:   ns,
						ServiceName: name,
					},
					EndpointURL: endpointURL,
					Confidence:  confidence,
					AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "prometheus"},
					Labels:      map[string]string{},
				})
			}
		}
	}

	// Hints path: emit External findings for any hint whose Kind is
	// "prometheus" or whose URL contains "prometheus".
	for _, hint := range env.Hints {
		if strings.ToLower(hint.Kind) != "prometheus" && !strings.Contains(strings.ToLower(hint.URL), "prometheus") {
			continue
		}
		findings = append(findings, discovery.Finding{
			Group: discovery.GroupProm,
			Kind:  "prometheus",
			Source: discovery.Source{
				External:    true,
				ExternalURL: hint.URL,
			},
			EndpointURL: hint.URL,
			Confidence:  1.0,
			AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone, Realm: "prometheus"},
			Labels:      map[string]string{},
		})
	}

	return findings
}

// promPort returns the port string to use for the proxy URL.
// It matches a port named "http" or "web", or any port with number 9090.
// The port string is always the numeric value to avoid collisions with the
// scheme component in the apiserver proxy URL format
// (/api/v1/namespaces/<ns>/services/<scheme>:<svc>:<port>/proxy).
// Returns ("", false) when no matching port is found.
func promPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if n == "http" || n == "web" || p.Port == 9090 {
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}
