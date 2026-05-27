package db

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/discovery"
)

func init() {
	discovery.Register(&pgDetector{})
	discovery.Register(&mysqlDetector{})
	discovery.Register(&redisDetector{})
}

// listServices is the seam used by tests to replace the real Kubernetes
// service-list call with a fake. Production code uses the default below.
var listServices = func(ctx context.Context, client *k8sclient.Client) (*corev1.ServiceList, error) {
	return client.Services(ctx, "", metav1.ListOptions{Limit: 500})
}

// k8sEndpointURL builds the custom k8s:// scheme URL for a TCP DB service.
// Downstream wiring recognises this scheme and opens a port-forward.
func k8sEndpointURL(ctxName, ns, svcName string, port int32) string {
	return fmt.Sprintf("k8s://%s/%s/%s:%d", ctxName, ns, svcName, port)
}

// --- postgres ----------------------------------------------------------------

type pgDetector struct{}

func (pgDetector) Name() string { return "tools.db.postgres" }

func (pgDetector) Detect(ctx context.Context, env discovery.Env) []discovery.Finding {
	const kind = "postgres"
	var findings []discovery.Finding

	if env.Hub != nil {
		for _, ctxName := range env.Hub.Names() {
			client, err := env.Hub.Get(ctxName)
			if err != nil {
				continue
			}
			svcList, err := listServices(ctx, client)
			if err != nil {
				continue
			}
			for _, svc := range svcList.Items {
				f, ok := detectPG(ctxName, svc)
				if ok {
					findings = append(findings, f)
				}
			}
		}
	}

	for _, hint := range env.DBHints {
		if strings.ToLower(hint.Kind) != kind {
			continue
		}
		findings = append(findings, discovery.Finding{
			Group: discovery.GroupDB,
			Kind:  kind,
			Source: discovery.Source{
				External:    true,
				ExternalURL: hint.DSN,
			},
			EndpointURL: hint.DSN,
			Confidence:  1.0,
			AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: kind},
			Labels:      map[string]string{},
		})
	}
	return findings
}

func detectPG(ctxName string, svc corev1.Service) (discovery.Finding, bool) {
	nameLower := strings.ToLower(svc.Name)
	appLabel := strings.ToLower(svc.Labels["app.kubernetes.io/name"])

	nameMatch := strings.Contains(nameLower, "postgres") || strings.Contains(nameLower, "postgresql")
	labelMatch := appLabel == "postgresql" || appLabel == "postgres"

	port, ok := pgPort(svc.Spec.Ports)
	if !ok {
		return discovery.Finding{}, false
	}
	if !nameMatch && !labelMatch {
		return discovery.Finding{}, false
	}

	var confidence float64
	switch {
	case (nameMatch || labelMatch) && labelMatch && nameMatch:
		confidence = 1.0
	case nameMatch && port != 0:
		confidence = 0.8
	case labelMatch && port != 0:
		confidence = 0.8
	default:
		confidence = 0.4
	}

	return discovery.Finding{
		Group: discovery.GroupDB,
		Kind:  "postgres",
		Source: discovery.Source{
			Context:     ctxName,
			Namespace:   svc.Namespace,
			ServiceName: svc.Name,
		},
		EndpointURL: k8sEndpointURL(ctxName, svc.Namespace, svc.Name, port),
		Confidence:  confidence,
		AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: "postgres"},
		Labels:      map[string]string{},
	}, true
}

func pgPort(ports []corev1.ServicePort) (int32, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 5432 || n == "tcp-postgresql" || n == "postgres" {
			return p.Port, true
		}
	}
	return 0, false
}

// --- mysql -------------------------------------------------------------------

type mysqlDetector struct{}

func (mysqlDetector) Name() string { return "tools.db.mysql" }

func (mysqlDetector) Detect(ctx context.Context, env discovery.Env) []discovery.Finding {
	const kind = "mysql"
	var findings []discovery.Finding

	if env.Hub != nil {
		for _, ctxName := range env.Hub.Names() {
			client, err := env.Hub.Get(ctxName)
			if err != nil {
				continue
			}
			svcList, err := listServices(ctx, client)
			if err != nil {
				continue
			}
			for _, svc := range svcList.Items {
				f, ok := detectMySQL(ctxName, svc)
				if ok {
					findings = append(findings, f)
				}
			}
		}
	}

	for _, hint := range env.DBHints {
		if strings.ToLower(hint.Kind) != kind {
			continue
		}
		findings = append(findings, discovery.Finding{
			Group: discovery.GroupDB,
			Kind:  kind,
			Source: discovery.Source{
				External:    true,
				ExternalURL: hint.DSN,
			},
			EndpointURL: hint.DSN,
			Confidence:  1.0,
			AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: kind},
			Labels:      map[string]string{},
		})
	}
	return findings
}

func detectMySQL(ctxName string, svc corev1.Service) (discovery.Finding, bool) {
	nameLower := strings.ToLower(svc.Name)
	appLabel := strings.ToLower(svc.Labels["app.kubernetes.io/name"])

	nameMatch := strings.Contains(nameLower, "mysql") || strings.Contains(nameLower, "mariadb")
	labelMatch := appLabel == "mysql" || appLabel == "mariadb" || appLabel == "percona-server"

	port, ok := mysqlPort(svc.Spec.Ports)
	if !ok {
		return discovery.Finding{}, false
	}
	if !nameMatch && !labelMatch {
		return discovery.Finding{}, false
	}

	var confidence float64
	switch {
	case nameMatch && labelMatch:
		confidence = 1.0
	case nameMatch && port != 0:
		confidence = 0.8
	case labelMatch && port != 0:
		confidence = 0.8
	default:
		confidence = 0.4
	}

	return discovery.Finding{
		Group: discovery.GroupDB,
		Kind:  "mysql",
		Source: discovery.Source{
			Context:     ctxName,
			Namespace:   svc.Namespace,
			ServiceName: svc.Name,
		},
		EndpointURL: k8sEndpointURL(ctxName, svc.Namespace, svc.Name, port),
		Confidence:  confidence,
		AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: "mysql"},
		Labels:      map[string]string{},
	}, true
}

func mysqlPort(ports []corev1.ServicePort) (int32, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 3306 || n == "mysql" || n == "tcp-mysql" {
			return p.Port, true
		}
	}
	return 0, false
}

// --- redis -------------------------------------------------------------------

type redisDetector struct{}

func (redisDetector) Name() string { return "tools.db.redis" }

func (redisDetector) Detect(ctx context.Context, env discovery.Env) []discovery.Finding {
	const kind = "redis"
	var findings []discovery.Finding

	if env.Hub != nil {
		for _, ctxName := range env.Hub.Names() {
			client, err := env.Hub.Get(ctxName)
			if err != nil {
				continue
			}
			svcList, err := listServices(ctx, client)
			if err != nil {
				continue
			}
			for _, svc := range svcList.Items {
				f, ok := detectRedis(ctxName, svc)
				if ok {
					findings = append(findings, f)
				}
			}
		}
	}

	for _, hint := range env.DBHints {
		if strings.ToLower(hint.Kind) != kind {
			continue
		}
		findings = append(findings, discovery.Finding{
			Group: discovery.GroupDB,
			Kind:  kind,
			Source: discovery.Source{
				External:    true,
				ExternalURL: hint.DSN,
			},
			EndpointURL: hint.DSN,
			Confidence:  1.0,
			AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: kind},
			Labels:      map[string]string{},
		})
	}
	return findings
}

func detectRedis(ctxName string, svc corev1.Service) (discovery.Finding, bool) {
	nameLower := strings.ToLower(svc.Name)
	appLabel := strings.ToLower(svc.Labels["app.kubernetes.io/name"])

	nameMatch := strings.Contains(nameLower, "redis") || strings.Contains(nameLower, "valkey")
	labelMatch := appLabel == "redis" || appLabel == "valkey" || appLabel == "keydb"

	port, ok := redisPort(svc.Spec.Ports)
	if !ok {
		return discovery.Finding{}, false
	}
	if !nameMatch && !labelMatch {
		return discovery.Finding{}, false
	}

	var confidence float64
	switch {
	case nameMatch && labelMatch:
		confidence = 1.0
	case nameMatch && port != 0:
		confidence = 0.8
	case labelMatch && port != 0:
		confidence = 0.8
	default:
		confidence = 0.4
	}

	return discovery.Finding{
		Group: discovery.GroupDB,
		Kind:  "redis",
		Source: discovery.Source{
			Context:     ctxName,
			Namespace:   svc.Namespace,
			ServiceName: svc.Name,
		},
		EndpointURL: k8sEndpointURL(ctxName, svc.Namespace, svc.Name, port),
		Confidence:  confidence,
		AuthHint:    discovery.AuthHint{Kind: discovery.AuthPassword, Realm: "redis"},
		Labels:      map[string]string{},
	}, true
}

func redisPort(ports []corev1.ServicePort) (int32, bool) {
	for _, p := range ports {
		n := strings.ToLower(p.Name)
		if p.Port == 6379 || n == "redis" || n == "tcp-redis" {
			return p.Port, true
		}
	}
	return 0, false
}
