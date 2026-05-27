package trace_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
	"github.com/rlaope/cloudy/internal/tools/trace"
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
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"test-ctx": {}}, "test-ctx")
}

func traceDetector(t *testing.T) discovery.Detector {
	t.Helper()
	for _, d := range discovery.All() {
		if d.Name() == "tools.trace" {
			return d
		}
	}
	t.Fatal("tools.trace detector not registered")
	return nil
}

// TestDetect_Tempo verifies that a service named "tempo" with port 3200 in
// namespace "monitoring" produces a Finding with Kind="tempo".
func TestDetect_Tempo(t *testing.T) {
	svcList := &corev1.ServiceList{
		Items: []corev1.Service{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tempo",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "tempo"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Name: "http", Port: 3200}},
				},
			},
		},
	}
	restore := trace.SetListServicesFn(func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return svcList, nil
	})
	defer restore()

	env := discovery.Env{Hub: newEmptyHub(), Proxy: newFakeProxy(t)}
	findings := traceDetector(t).Detect(context.Background(), env)

	var found *discovery.Finding
	for i := range findings {
		if findings[i].Kind == "tempo" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a tempo finding, got %+v", findings)
	}
	if found.Group != discovery.GroupTrace {
		t.Errorf("Group = %q, want %q", found.Group, discovery.GroupTrace)
	}
	if found.Source.Namespace != "monitoring" {
		t.Errorf("Source.Namespace = %q, want monitoring", found.Source.Namespace)
	}
	if found.Source.ServiceName != "tempo" {
		t.Errorf("Source.ServiceName = %q, want tempo", found.Source.ServiceName)
	}
	want := "services/http:tempo:3200/proxy"
	if !strings.Contains(found.EndpointURL, want) {
		t.Errorf("EndpointURL %q does not contain %q", found.EndpointURL, want)
	}
	if found.Confidence == 0 {
		t.Error("expected non-zero Confidence")
	}
	if found.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// TestDetect_Jaeger verifies that a service named "jaeger-query" with port
// 16686 produces a Finding with Kind="jaeger".
func TestDetect_Jaeger(t *testing.T) {
	svcList := &corev1.ServiceList{
		Items: []corev1.Service{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "jaeger-query",
					Namespace: "tracing",
					Labels:    map[string]string{"app.kubernetes.io/name": "jaeger-query"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Name: "http", Port: 16686}},
				},
			},
		},
	}
	restore := trace.SetListServicesFn(func(_ context.Context, _ *k8sclient.Client) (*corev1.ServiceList, error) {
		return svcList, nil
	})
	defer restore()

	env := discovery.Env{Hub: newEmptyHub(), Proxy: newFakeProxy(t)}
	findings := traceDetector(t).Detect(context.Background(), env)

	var found *discovery.Finding
	for i := range findings {
		if findings[i].Kind == "jaeger" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a jaeger finding, got %+v", findings)
	}
	if found.Group != discovery.GroupTrace {
		t.Errorf("Group = %q, want %q", found.Group, discovery.GroupTrace)
	}
	if found.Source.ServiceName != "jaeger-query" {
		t.Errorf("Source.ServiceName = %q, want jaeger-query", found.Source.ServiceName)
	}
	want := "services/http:jaeger-query:16686/proxy"
	if !strings.Contains(found.EndpointURL, want) {
		t.Errorf("EndpointURL %q does not contain %q", found.EndpointURL, want)
	}
	if found.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// TestDetect_HintTempo verifies that an Env.Hints entry with Kind="tempo"
// produces an External finding without requiring a Hub/Proxy.
func TestDetect_HintTempo(t *testing.T) {
	env := discovery.Env{
		Hints: []config.HTTPEndpoint{
			{Name: "ext-tempo", Kind: "tempo", URL: "http://tempo.example.com:3200"},
		},
	}
	findings := traceDetector(t).Detect(context.Background(), env)

	if len(findings) == 0 {
		t.Fatal("expected at least one finding from hint")
	}
	f := findings[0]
	if !f.Source.External {
		t.Error("Source.External should be true")
	}
	if f.Kind != "tempo" {
		t.Errorf("Kind = %q, want tempo", f.Kind)
	}
	if f.EndpointURL != "http://tempo.example.com:3200" {
		t.Errorf("EndpointURL = %q, want http://tempo.example.com:3200", f.EndpointURL)
	}
	if f.Labels == nil {
		t.Error("Labels must be non-nil")
	}
}

// TestDetect_RegistryContainsTrace verifies discovery.All() includes
// "tools.trace" after the package is imported.
func TestDetect_RegistryContainsTrace(t *testing.T) {
	for _, d := range discovery.All() {
		if d.Name() == "tools.trace" {
			return
		}
	}
	t.Error("tools.trace detector not found in discovery.All()")
}
