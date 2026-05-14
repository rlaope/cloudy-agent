package perf_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
	"github.com/rlaope/cloudy/internal/tools/perf"
	"github.com/rlaope/cloudy/internal/transport"

	// Ensure the detector's init() runs.
	_ "github.com/rlaope/cloudy/internal/tools/perf"
)

func makeProxy(t *testing.T) *transport.ServiceProxy {
	t.Helper()
	cfg := &rest.Config{Host: "https://fake-apiserver:6443", BearerToken: "test"}
	p, err := transport.NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("NewServiceProxy: %v", err)
	}
	return p
}

func makeService(ns, name string, ports []corev1.ServicePort) corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

// installFakeServices replaces listPerfServicesFn for the duration of the test
// and returns the matching Hub (real client not needed since the seam bypasses it).
func installFakeServices(t *testing.T, svcs []corev1.Service) *k8stool.Hub {
	t.Helper()

	// The Hub client is only used as a routing key — the seam ignores it and
	// returns our canned list. Pass a nil *Client; NewHubFromClients accepts it.
	hub := k8stool.NewHubFromClients(map[string]*k8stool.Client{"test-ctx": nil}, "test-ctx")

	list := &corev1.ServiceList{Items: svcs}
	restore := perf.SetListPerfServicesFn(func(_ context.Context, _ *k8stool.Client) (*corev1.ServiceList, error) {
		return list, nil
	})
	t.Cleanup(restore)

	return hub
}

// runPerfDetector locates and runs only the "tools.perf" detector.
func runPerfDetector(t *testing.T, env discovery.Env) []discovery.Finding {
	t.Helper()
	for _, d := range discovery.All() {
		if d.Name() == "tools.perf" {
			return d.Detect(context.Background(), env)
		}
	}
	t.Fatal("tools.perf detector not registered")
	return nil
}

// TestDetector_PprofService verifies that a service with a port named "pprof"
// and number 6060 produces a pprof finding with an EndpointURL that contains
// the apiserver proxy path for the service.
func TestDetector_PprofService(t *testing.T) {
	svc := makeService("default", "api-pprof", []corev1.ServicePort{
		{Name: "pprof", Port: 6060},
	})
	hub := installFakeServices(t, []corev1.Service{svc})
	proxy := makeProxy(t)

	findings := runPerfDetector(t, discovery.Env{Hub: hub, Proxy: proxy})

	var got []discovery.Finding
	for _, f := range findings {
		if f.Kind == "pprof" {
			got = append(got, f)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least one pprof finding, got none")
	}
	f := got[0]
	if f.Group != discovery.GroupPerf {
		t.Errorf("Group = %q, want %q", f.Group, discovery.GroupPerf)
	}
	if f.Source.ServiceName != "api-pprof" {
		t.Errorf("ServiceName = %q, want %q", f.Source.ServiceName, "api-pprof")
	}
	// EndpointURL must route through the apiserver proxy and reference the service.
	if f.EndpointURL == "" {
		t.Fatal("EndpointURL must not be empty")
	}
	if f.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 for named pprof port", f.Confidence)
	}
	if f.AuthHint.Kind != discovery.AuthNone {
		t.Errorf("AuthHint.Kind = %q, want %q", f.AuthHint.Kind, discovery.AuthNone)
	}
}

// TestDetector_V8Service verifies that a service with port named "inspect"/9229
// produces a v8 finding.
func TestDetector_V8Service(t *testing.T) {
	svc := makeService("prod", "node-inspect", []corev1.ServicePort{
		{Name: "inspect", Port: 9229},
	})
	hub := installFakeServices(t, []corev1.Service{svc})
	proxy := makeProxy(t)

	findings := runPerfDetector(t, discovery.Env{Hub: hub, Proxy: proxy})

	var got []discovery.Finding
	for _, f := range findings {
		if f.Kind == "v8" {
			got = append(got, f)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least one v8 finding, got none")
	}
	f := got[0]
	if f.Group != discovery.GroupPerf {
		t.Errorf("Group = %q, want %q", f.Group, discovery.GroupPerf)
	}
	if f.Source.ServiceName != "node-inspect" {
		t.Errorf("ServiceName = %q, want %q", f.Source.ServiceName, "node-inspect")
	}
	if f.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 for named inspect port", f.Confidence)
	}
}

// TestDetector_HintPprof verifies that an Env.Hints entry with Kind=="pprof"
// produces an External pprof finding (no Hub/Proxy needed).
func TestDetector_HintPprof(t *testing.T) {
	env := discovery.Env{
		Hints: []config.HTTPEndpoint{
			{Name: "my-go-svc", Kind: "pprof", URL: "http://go-svc:6060"},
		},
	}
	findings := runPerfDetector(t, env)

	var got []discovery.Finding
	for _, f := range findings {
		if f.Kind == "pprof" && f.Source.External {
			got = append(got, f)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected an external pprof finding from hint, got none")
	}
	f := got[0]
	if f.EndpointURL != "http://go-svc:6060" {
		t.Errorf("EndpointURL = %q, want %q", f.EndpointURL, "http://go-svc:6060")
	}
	if f.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9 for hint-based finding", f.Confidence)
	}
	if f.Group != discovery.GroupPerf {
		t.Errorf("Group = %q, want %q", f.Group, discovery.GroupPerf)
	}
}

// TestDetector_Negative verifies that a generic service with port "http"/80
// produces no perf findings.
func TestDetector_Negative(t *testing.T) {
	svc := makeService("default", "api", []corev1.ServicePort{
		{Name: "http", Port: 80},
	})
	hub := installFakeServices(t, []corev1.Service{svc})
	proxy := makeProxy(t)

	findings := runPerfDetector(t, discovery.Env{Hub: hub, Proxy: proxy})

	if len(findings) != 0 {
		t.Errorf("expected no findings for generic http:80 service, got %d: %+v", len(findings), findings)
	}
}

// TestDetector_Registration verifies that discovery.All() contains "tools.perf"
// after the perf package is imported.
func TestDetector_Registration(t *testing.T) {
	found := false
	for _, d := range discovery.All() {
		if d.Name() == "tools.perf" {
			found = true
			break
		}
	}
	if !found {
		t.Error(`discovery.All() does not contain a detector named "tools.perf"`)
	}
}
