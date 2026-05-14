package log

import (
	"context"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/tools/k8s"
)

func init() { discovery.Register(&logDetector{}) }

type logDetector struct{}

func (logDetector) Name() string { return "tools.log" }

// listServicesFn is the seam used by tests to replace the real Kubernetes
// service-list call with a fake. Production code uses defaultListServices.
var listServicesFn = defaultListServices

// defaultListServices lists services across all namespaces for a given client.
func defaultListServices(ctx context.Context, client *k8s.Client) (*corev1.ServiceList, error) {
	return client.Services(ctx, "", metav1.ListOptions{Limit: 500})
}

func (logDetector) Detect(ctx context.Context, env discovery.Env) []discovery.Finding {
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

				// --- Loki ---
				lokiNameMatch := strings.Contains(nameLower, "loki")
				lokiLabelMatch := appLabel == "loki" || appLabel == "loki-gateway" ||
					appLabel == "loki-read" || appLabel == "loki-write"

				if lokiPortStr, ok := lokiPort(svc.Spec.Ports); ok {
					endpointURL := env.Proxy.URL(ns, name, "http", lokiPortStr, "")
					if endpointURL != "" {
						var confidence float64
						switch {
						case lokiNameMatch && lokiLabelMatch:
							confidence = 1.0
						case lokiNameMatch || lokiLabelMatch:
							confidence = 0.8
						default:
							confidence = 0.4
						}
						findings = append(findings, discovery.Finding{
							Group:       discovery.GroupLog,
							Kind:        "loki",
							Source:      discovery.Source{Context: ctxName, Namespace: ns, ServiceName: name},
							EndpointURL: endpointURL,
							Confidence:  confidence,
							AuthHint:    discovery.AuthHint{Kind: discovery.AuthBearer, Realm: "loki"},
							Labels:      map[string]string{},
						})
					}
				}

				// --- Elasticsearch ---
				esNameMatch := strings.Contains(nameLower, "elasticsearch") ||
					strings.Contains(nameLower, "elastic")
				esLabelMatch := appLabel == "elasticsearch"

				if esNameMatch || esLabelMatch {
					if esPortStr, ok := esPort(svc.Spec.Ports); ok {
						endpointURL := env.Proxy.URL(ns, name, "http", esPortStr, "")
						if endpointURL != "" {
							var confidence float64
							switch {
							case esNameMatch && esLabelMatch:
								confidence = 1.0
							default:
								confidence = 0.8
							}
							findings = append(findings, discovery.Finding{
								Group:       discovery.GroupLog,
								Kind:        "elasticsearch",
								Source:      discovery.Source{Context: ctxName, Namespace: ns, ServiceName: name},
								EndpointURL: endpointURL,
								Confidence:  confidence,
								AuthHint:    discovery.AuthHint{Kind: discovery.AuthBasic, Realm: "elasticsearch"},
								Labels:      map[string]string{},
							})
						}
					}
				}
			}
		}
	}

	// Hints path: emit External findings for any hint whose Kind is
	// "loki" or "elasticsearch".
	for _, hint := range env.Hints {
		k := strings.ToLower(hint.Kind)
		switch k {
		case "loki":
			findings = append(findings, discovery.Finding{
				Group: discovery.GroupLog,
				Kind:  "loki",
				Source: discovery.Source{
					External:    true,
					ExternalURL: hint.URL,
				},
				EndpointURL: hint.URL,
				Confidence:  1.0,
				AuthHint:    discovery.AuthHint{Kind: discovery.AuthBearer, Realm: "loki"},
				Labels:      map[string]string{},
			})
		case "elasticsearch", "es", "opensearch":
			findings = append(findings, discovery.Finding{
				Group: discovery.GroupLog,
				Kind:  "elasticsearch",
				Source: discovery.Source{
					External:    true,
					ExternalURL: hint.URL,
				},
				EndpointURL: hint.URL,
				Confidence:  1.0,
				AuthHint:    discovery.AuthHint{Kind: discovery.AuthBasic, Realm: "elasticsearch"},
				Labels:      map[string]string{},
			})
		}
	}

	return findings
}

// lokiPort returns the port string to use for the proxy URL.
// Prefers port 3100, or ports named "http-metrics" or "loki-http".
func lokiPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 3100 || n == "http-metrics" || n == "loki-http" {
			if p.Name != "" {
				return p.Name, true
			}
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// esPort returns the port string to use for the proxy URL.
// Prefers port 9200, or a port named "http".
func esPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 9200 || n == "http" {
			if p.Name != "" {
				return p.Name, true
			}
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}
