package perf

import (
	"context"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/tools/k8s"
)

func init() { discovery.Register(&detector{}) }

type detector struct{}

func (detector) Name() string { return "tools.perf" }

// listPerfServicesFn is the seam used by tests to replace the real Kubernetes
// service-list call with a fake. Production code uses defaultListPerfServices.
var listPerfServicesFn = defaultListPerfServices

// defaultListPerfServices lists services across all namespaces for a given client.
func defaultListPerfServices(ctx context.Context, client *k8s.Client) (*corev1.ServiceList, error) {
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

			svcList, err := listPerfServicesFn(ctx, client)
			if err != nil {
				continue
			}

			for _, svc := range svcList.Items {
				ns := svc.Namespace
				name := svc.Name

				// pprof: port named "pprof"/"debug" or number 6060.
				if portStr, ok := pprofPort(svc.Spec.Ports); ok {
					endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
					if endpointURL != "" {
						confidence := pprofConfidence(name, portStr)
						findings = append(findings, discovery.Finding{
							Group: discovery.GroupPerf,
							Kind:  "pprof",
							Source: discovery.Source{
								Context:     ctxName,
								Namespace:   ns,
								ServiceName: name,
							},
							EndpointURL: endpointURL,
							Confidence:  confidence,
							AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone},
							Labels:      map[string]string{},
						})
					}
				}

				// v8: port named "inspect"/"node-inspect" or number 9229.
				if portStr, ok := v8Port(svc.Spec.Ports); ok {
					endpointURL := env.Proxy.URL(ns, name, "http", portStr, "")
					if endpointURL != "" {
						confidence := v8Confidence(name, portStr)
						findings = append(findings, discovery.Finding{
							Group: discovery.GroupPerf,
							Kind:  "v8",
							Source: discovery.Source{
								Context:     ctxName,
								Namespace:   ns,
								ServiceName: name,
							},
							EndpointURL: endpointURL,
							Confidence:  confidence,
							AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone},
							Labels:      map[string]string{},
						})
					}
				}
			}
		}
	}

	// Hints path: emit External findings for any hint whose Kind or URL hints
	// at pprof or node-inspector / v8.
	for _, hint := range env.Hints {
		kindLower := strings.ToLower(hint.Kind)
		urlLower := strings.ToLower(hint.URL)

		switch {
		case kindLower == "pprof" || strings.Contains(urlLower, "pprof") || strings.Contains(urlLower, "debug/pprof"):
			findings = append(findings, discovery.Finding{
				Group: discovery.GroupPerf,
				Kind:  "pprof",
				Source: discovery.Source{
					External:    true,
					ExternalURL: hint.URL,
				},
				EndpointURL: hint.URL,
				Confidence:  0.9,
				AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone},
				Labels:      map[string]string{},
			})
		case kindLower == "node-inspector" || kindLower == "v8" || strings.Contains(urlLower, "json/list") || strings.Contains(urlLower, ":9229"):
			findings = append(findings, discovery.Finding{
				Group: discovery.GroupPerf,
				Kind:  "v8",
				Source: discovery.Source{
					External:    true,
					ExternalURL: hint.URL,
				},
				EndpointURL: hint.URL,
				Confidence:  0.9,
				AuthHint:    discovery.AuthHint{Kind: discovery.AuthNone},
				Labels:      map[string]string{},
			})
		}
	}

	return findings
}

// pprofPort returns the port string to use for the proxy URL.
// It matches a port named "pprof" or "debug", or with number 6060.
// Returns the port Name when set, else the numeric port as string.
// Returns ("", false) when no matching port is found.
func pprofPort(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if n == "pprof" || n == "debug" || p.Port == 6060 {
			if p.Name != "" {
				return p.Name, true
			}
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// v8Port returns the port string to use for the proxy URL.
// It matches a port named "inspect" or "node-inspect", or with number 9229.
// Returns the port Name when set, else the numeric port as string.
// Returns ("", false) when no matching port is found.
func v8Port(ports []corev1.ServicePort) (string, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if n == "inspect" || n == "node-inspect" || p.Port == 9229 {
			if p.Name != "" {
				return p.Name, true
			}
			return strconv.Itoa(int(p.Port)), true
		}
	}
	return "", false
}

// pprofConfidence returns 1.0 when the port name explicitly signals pprof,
// and 0.6 when we matched only by port number.
func pprofConfidence(svcName, portStr string) float64 {
	n := strings.ToLower(portStr)
	if n == "pprof" || n == "debug" {
		return 1.0
	}
	// Port name may be numeric ("6060"); also check svc name as secondary signal.
	if strings.Contains(strings.ToLower(svcName), "pprof") {
		return 1.0
	}
	return 0.6
}

// v8Confidence returns 1.0 when the port name explicitly signals v8 inspection,
// and 0.6 when we matched only by port number.
func v8Confidence(svcName, portStr string) float64 {
	n := strings.ToLower(portStr)
	if n == "inspect" || n == "node-inspect" {
		return 1.0
	}
	if strings.Contains(strings.ToLower(svcName), "inspect") {
		return 1.0
	}
	return 0.6
}
