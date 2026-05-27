package prom_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools/prom"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/transport"
)

// newFakeProxy builds a *transport.ServiceProxy pointing at a placeholder
// host. The URL method is pure string manipulation — no real server is needed.
func newFakeProxy(t *testing.T) *transport.ServiceProxy {
	t.Helper()
	cfg := &rest.Config{Host: "https://fake-apiserver:6443"}
	p, err := transport.NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("newFakeProxy: %v", err)
	}
	return p
}

// newEmptyHub builds a single-context Hub with no real kubeconfig. Used only
// to satisfy env.Hub != nil; the listServicesFn seam replaces the actual API
// call so the Hub never contacts a cluster.
func newEmptyHub() *k8sclient.Hub {
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{}, "")
}

func promDetector(t *testing.T) discovery.Detector {
	t.Helper()
	for _, d := range discovery.All() {
		if d.Name() == "tools.prom" {
			return d
		}
	}
	t.Fatal("tools.prom detector not registered")
	return nil
}

// Test 1: K8s service named "prometheus-server" in ns "monitoring" with port
// 9090 emits one Finding with the expected fields and proxy URL.
func TestDetect_K8sServiceByName(t *testing.T) {
	svcList := &corev1.ServiceList{
		Items: []corev1.Service{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-server",
					Namespace: "monitoring",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Name: "http", Port: 9090}},
				},
			},
		},
	}
	restore := prom.SetListServicesFn(func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return svcList, nil
	})
	defer restore()

	hub := k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"test-ctx": {}}, "test-ctx")
	env := discovery.Env{Hub: hub, Proxy: newFakeProxy(t)}

	found := promDetector(t).Detect(context.Background(), env)

	if len(found) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(found), found)
	}
	f := found[0]
	if f.Group != discovery.GroupProm {
		t.Errorf("Group = %q, want %q", f.Group, discovery.GroupProm)
	}
	if f.Kind != "prometheus" {
		t.Errorf("Kind = %q, want %q", f.Kind, "prometheus")
	}
	if f.Source.Namespace != "monitoring" {
		t.Errorf("Source.Namespace = %q, want %q", f.Source.Namespace, "monitoring")
	}
	if f.Source.ServiceName != "prometheus-server" {
		t.Errorf("Source.ServiceName = %q, want %q", f.Source.ServiceName, "prometheus-server")
	}
	want := "services/http:prometheus-server:9090/proxy"
	if !strings.Contains(f.EndpointURL, want) {
		t.Errorf("EndpointURL %q does not contain %q", f.EndpointURL, want)
	}
	if f.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// Test 2: env.Hints with Kind="prometheus" URL emits an External finding.
func TestDetect_HintKindPrometheus(t *testing.T) {
	env := discovery.Env{
		Hints: []config.HTTPEndpoint{
			{Kind: "prometheus", URL: "https://prom.example/"},
		},
	}

	found := promDetector(t).Detect(context.Background(), env)

	if len(found) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(found))
	}
	f := found[0]
	if !f.Source.External {
		t.Error("Source.External should be true")
	}
	if f.Source.ExternalURL != "https://prom.example/" {
		t.Errorf("Source.ExternalURL = %q, want %q", f.Source.ExternalURL, "https://prom.example/")
	}
	if f.EndpointURL != "https://prom.example/" {
		t.Errorf("EndpointURL = %q, want %q", f.EndpointURL, "https://prom.example/")
	}
	if f.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// Test 3: Hub==nil and no hints → returns nil/empty slice.
func TestDetect_NilHubNoHints(t *testing.T) {
	env := discovery.Env{}
	found := promDetector(t).Detect(context.Background(), env)
	if len(found) != 0 {
		t.Errorf("expected 0 findings, got %d", len(found))
	}
}

// Test 4: Registry side-effect — importing the prom package registers "tools.prom".
func TestDetect_RegistryContainsProm(t *testing.T) {
	found := false
	for _, d := range discovery.All() {
		if d.Name() == "tools.prom" {
			found = true
			break
		}
	}
	if !found {
		t.Error("tools.prom detector not found in discovery.All()")
	}
}
