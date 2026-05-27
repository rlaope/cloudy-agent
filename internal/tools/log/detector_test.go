package log_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/tools/log"
	"github.com/rlaope/cloudy/internal/transport"
)

// newFakeProxy builds a minimal *transport.ServiceProxy pointing at a
// placeholder host. The URL method is pure string manipulation, so no real
// server is needed.
func newFakeProxy(t *testing.T) *transport.ServiceProxy {
	t.Helper()
	cfg := &rest.Config{Host: "https://fake-apiserver:6443"}
	p, err := transport.NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("newFakeProxy: %v", err)
	}
	return p
}

// newFakeHub builds a single-context Hub with a nil client. The client is
// never actually called because the listServicesFn seam is replaced in tests.
func newFakeHub() *k8sclient.Hub {
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"test-ctx": nil}, "test-ctx")
}

func svcList(svcs ...*corev1.Service) *corev1.ServiceList {
	items := make([]corev1.Service, 0, len(svcs))
	for _, s := range svcs {
		items = append(items, *s)
	}
	return &corev1.ServiceList{Items: items}
}

func svc(ns, name string, labels map[string]string, ports []corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{Ports: ports},
	}
}

func logDetector(t *testing.T) discovery.Detector {
	t.Helper()
	for _, d := range discovery.All() {
		if d.Name() == "tools.log" {
			return d
		}
	}
	t.Fatal("tools.log detector not registered")
	return nil
}

// TestDetect_LokiByName verifies that a service named "loki-gateway" with
// port 3100 in namespace "monitoring" emits a Finding with Kind="loki" and
// AuthHint.Kind=AuthBearer.
func TestDetect_LokiByName(t *testing.T) {
	sl := svcList(svc("monitoring", "loki-gateway", nil, []corev1.ServicePort{
		{Port: 3100},
	}))
	restore := log.SetListServicesFn(func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return sl, nil
	})
	defer restore()

	env := discovery.Env{Hub: newFakeHub(), Proxy: newFakeProxy(t)}
	findings := logDetector(t).Detect(context.Background(), env)

	var found *discovery.Finding
	for i := range findings {
		if findings[i].Kind == "loki" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a loki finding, got %+v", findings)
	}
	if found.Group != discovery.GroupLog {
		t.Errorf("Group = %q, want %q", found.Group, discovery.GroupLog)
	}
	if found.Source.Namespace != "monitoring" {
		t.Errorf("Source.Namespace = %q, want monitoring", found.Source.Namespace)
	}
	if found.Source.ServiceName != "loki-gateway" {
		t.Errorf("Source.ServiceName = %q, want loki-gateway", found.Source.ServiceName)
	}
	if found.AuthHint.Kind != discovery.AuthBearer {
		t.Errorf("AuthHint.Kind = %q, want %q", found.AuthHint.Kind, discovery.AuthBearer)
	}
	if found.Confidence == 0 {
		t.Error("expected non-zero Confidence")
	}
	if found.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// TestDetect_ElasticsearchByName verifies that a service named
// "elasticsearch-master" with port 9200 emits a Finding with
// Kind="elasticsearch" and AuthHint.Kind=AuthBasic.
func TestDetect_ElasticsearchByName(t *testing.T) {
	sl := svcList(svc("logging", "elasticsearch-master", nil, []corev1.ServicePort{
		{Port: 9200},
	}))
	restore := log.SetListServicesFn(func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return sl, nil
	})
	defer restore()

	env := discovery.Env{Hub: newFakeHub(), Proxy: newFakeProxy(t)}
	findings := logDetector(t).Detect(context.Background(), env)

	var found *discovery.Finding
	for i := range findings {
		if findings[i].Kind == "elasticsearch" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected an elasticsearch finding, got %+v", findings)
	}
	if found.Group != discovery.GroupLog {
		t.Errorf("Group = %q, want %q", found.Group, discovery.GroupLog)
	}
	if found.Source.Namespace != "logging" {
		t.Errorf("Source.Namespace = %q, want logging", found.Source.Namespace)
	}
	if found.Source.ServiceName != "elasticsearch-master" {
		t.Errorf("Source.ServiceName = %q, want elasticsearch-master", found.Source.ServiceName)
	}
	if found.AuthHint.Kind != discovery.AuthBasic {
		t.Errorf("AuthHint.Kind = %q, want %q", found.AuthHint.Kind, discovery.AuthBasic)
	}
	if found.Confidence == 0 {
		t.Error("expected non-zero Confidence")
	}
	if found.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// TestDetect_HintLoki verifies that an Env.Hints entry with Kind="loki" emits
// an External finding.
func TestDetect_HintLoki(t *testing.T) {
	env := discovery.Env{
		Hints: []config.HTTPEndpoint{
			{Name: "ext-loki", Kind: "loki", URL: "http://loki.example.com:3100"},
		},
	}
	findings := logDetector(t).Detect(context.Background(), env)

	if len(findings) == 0 {
		t.Fatal("expected at least one finding from hint")
	}
	f := findings[0]
	if !f.Source.External {
		t.Error("expected finding.Source.External=true")
	}
	if f.Kind != "loki" {
		t.Errorf("expected Kind=loki, got %q", f.Kind)
	}
	if f.EndpointURL != "http://loki.example.com:3100" {
		t.Errorf("unexpected EndpointURL %q", f.EndpointURL)
	}
}

// TestDetect_Registration verifies discovery.All() includes "tools.log" after
// the package is imported.
func TestDetect_Registration(t *testing.T) {
	for _, d := range discovery.All() {
		if d.Name() == "tools.log" {
			return
		}
	}
	t.Error("tools.log not found in discovery.All()")
}
