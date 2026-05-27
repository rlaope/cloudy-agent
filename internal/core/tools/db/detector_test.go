package db

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
)

// fakeServiceList replaces listServices with a function returning a fixed list.
func fakeServiceList(svcs []corev1.Service) func(ctx context.Context, client *k8sclient.Client) (*corev1.ServiceList, error) {
	return func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return &corev1.ServiceList{Items: svcs}, nil
	}
}

func svc(name, ns string, labels map[string]string, ports []corev1.ServicePort) corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{Ports: ports},
	}
}

func port(name string, number int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: number}
}

// fakeHub returns a *k8sclient.Hub with a single default context key "" so
// hub.Names() returns [""] and hub.Get("") returns a non-nil *k8sclient.Client
// (the seam client is never actually called — listServices is replaced).
func fakeHub() *k8sclient.Hub {
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": {}}, "")
}

// TestPostgresK8s verifies a service named "orders-db-postgresql" on port 5432
// in namespace "apps" produces a postgres finding with the correct k8s:// URL.
func TestPostgresK8s(t *testing.T) {
	orig := listServices
	t.Cleanup(func() { listServices = orig })

	listServices = fakeServiceList([]corev1.Service{
		svc("orders-db-postgresql", "apps", nil, []corev1.ServicePort{port("", 5432)}),
	})

	env := discovery.Env{Hub: fakeHub()}
	findings := (&pgDetector{}).Detect(context.Background(), env)

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Group != discovery.GroupDB {
		t.Errorf("Group: got %q, want %q", f.Group, discovery.GroupDB)
	}
	if f.Kind != "postgres" {
		t.Errorf("Kind: got %q, want %q", f.Kind, "postgres")
	}
	want := "k8s:///apps/orders-db-postgresql:5432"
	if f.EndpointURL != want {
		t.Errorf("EndpointURL: got %q, want %q", f.EndpointURL, want)
	}
}

// TestMySQLK8s verifies a service named "billing-mysql" on port 3306 yields a
// mysql finding.
func TestMySQLK8s(t *testing.T) {
	orig := listServices
	t.Cleanup(func() { listServices = orig })

	listServices = fakeServiceList([]corev1.Service{
		svc("billing-mysql", "billing", nil, []corev1.ServicePort{port("", 3306)}),
	})

	env := discovery.Env{Hub: fakeHub()}
	findings := (&mysqlDetector{}).Detect(context.Background(), env)

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Kind != "mysql" {
		t.Errorf("Kind: got %q, want %q", findings[0].Kind, "mysql")
	}
}

// TestRedisK8s verifies a service named "cache-redis-master" on port 6379
// yields a redis finding.
func TestRedisK8s(t *testing.T) {
	orig := listServices
	t.Cleanup(func() { listServices = orig })

	listServices = fakeServiceList([]corev1.Service{
		svc("cache-redis-master", "infra", nil, []corev1.ServicePort{port("", 6379)}),
	})

	env := discovery.Env{Hub: fakeHub()}
	findings := (&redisDetector{}).Detect(context.Background(), env)

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Kind != "redis" {
		t.Errorf("Kind: got %q, want %q", findings[0].Kind, "redis")
	}
}

// TestDBHintsExternal verifies that a DBHints entry with Kind="postgres" and a
// DSN is emitted as an External finding with EndpointURL=DSN.
func TestDBHintsExternal(t *testing.T) {
	dsn := "postgres://readonly@prod-db:5432/app?sslmode=disable"
	env := discovery.Env{
		DBHints: []config.DatabaseEndpoint{
			{Name: "prod", Kind: "postgres", DSN: dsn},
		},
	}

	findings := (&pgDetector{}).Detect(context.Background(), env)

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if !f.Source.External {
		t.Error("expected Source.External=true")
	}
	if f.EndpointURL != dsn {
		t.Errorf("EndpointURL: got %q, want %q", f.EndpointURL, dsn)
	}
	if f.Source.ExternalURL != dsn {
		t.Errorf("ExternalURL: got %q, want %q", f.Source.ExternalURL, dsn)
	}
}

// TestCrossPollination verifies that a postgres service is NOT matched by the
// mysql or redis detector, and vice versa.
func TestCrossPollination(t *testing.T) {
	orig := listServices
	t.Cleanup(func() { listServices = orig })

	pgSvc := svc("orders-db-postgresql", "apps", nil, []corev1.ServicePort{port("", 5432)})
	mySvc := svc("billing-mysql", "billing", nil, []corev1.ServicePort{port("", 3306)})
	rdSvc := svc("cache-redis-master", "infra", nil, []corev1.ServicePort{port("", 6379)})

	allSvcs := []corev1.Service{pgSvc, mySvc, rdSvc}
	listServices = fakeServiceList(allSvcs)
	env := discovery.Env{Hub: fakeHub()}

	pgFindings := (&pgDetector{}).Detect(context.Background(), env)
	myFindings := (&mysqlDetector{}).Detect(context.Background(), env)
	rdFindings := (&redisDetector{}).Detect(context.Background(), env)

	// postgres detector must not match mysql or redis services
	for _, f := range pgFindings {
		if strings.Contains(strings.ToLower(f.Source.ServiceName), "mysql") ||
			strings.Contains(strings.ToLower(f.Source.ServiceName), "redis") {
			t.Errorf("pgDetector matched non-postgres svc %q", f.Source.ServiceName)
		}
	}
	// mysql detector must not match postgres or redis services
	for _, f := range myFindings {
		if strings.Contains(strings.ToLower(f.Source.ServiceName), "postgres") ||
			strings.Contains(strings.ToLower(f.Source.ServiceName), "redis") {
			t.Errorf("mysqlDetector matched non-mysql svc %q", f.Source.ServiceName)
		}
	}
	// redis detector must not match postgres or mysql services
	for _, f := range rdFindings {
		if strings.Contains(strings.ToLower(f.Source.ServiceName), "postgres") ||
			strings.Contains(strings.ToLower(f.Source.ServiceName), "mysql") {
			t.Errorf("redisDetector matched non-redis svc %q", f.Source.ServiceName)
		}
	}
}

// TestRegistration verifies all three detector names appear in discovery.All().
func TestRegistration(t *testing.T) {
	names := make(map[string]bool)
	for _, d := range discovery.All() {
		names[d.Name()] = true
	}

	want := []string{"tools.db.postgres", "tools.db.mysql", "tools.db.redis"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("discovery.All() missing detector %q", w)
		}
	}
}
