package setup

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeScanner wraps the internal detection helpers for unit testing without a
// live API server. We test the classify / detect logic directly.

func makeNode(name string, labels map[string]string, gpuQty string) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{}},
	}
	if gpuQty != "" {
		n.Status.Allocatable["nvidia.com/gpu"] = resource.MustParse(gpuQty)
	}
	return n
}

func makePod(name, ns, image string, envs map[string]string) *corev1.Pod {
	var envVars []corev1.EnvVar
	for k, v := range envs {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Image: image, Env: envVars}},
		},
	}
}

func makeLabeledPod(name, ns, image string, labels map[string]string) *corev1.Pod {
	p := makePod(name, ns, image, nil)
	p.Labels = labels
	return p
}

// TestIsGPUNode_ByAllocatable verifies detection via nvidia.com/gpu allocatable.
func TestIsGPUNode_ByAllocatable(t *testing.T) {
	n := makeNode("gpu-node", nil, "4")
	if !isGPUNode(*n) {
		t.Error("expected GPU node via allocatable")
	}
}

// TestIsGPUNode_ByLabel verifies detection via node label.
func TestIsGPUNode_ByLabel(t *testing.T) {
	n := makeNode("labeled", map[string]string{"accelerator": "nvidia-tesla-v100"}, "")
	if !isGPUNode(*n) {
		t.Error("expected GPU node via label containing 'nvidia'")
	}
}

// TestIsGPUNode_Zero ensures a node with 0 GPU allocatable is not counted.
func TestIsGPUNode_Zero(t *testing.T) {
	n := makeNode("cpu-node", nil, "0")
	if isGPUNode(*n) {
		t.Error("expected non-GPU node for allocatable=0")
	}
}

// TestIsJVMPod_ByImage verifies JVM detection via image name.
func TestIsJVMPod_ByImage(t *testing.T) {
	for _, img := range []string{"eclipse-temurin:17-jdk", "openjdk:11", "myapp:2-jre"} {
		p := makePod("p", "default", img, nil)
		if !isJVMPod(*p) {
			t.Errorf("expected JVM pod for image %q", img)
		}
	}
}

// TestIsJVMPod_ByEnv verifies JVM detection via environment variable.
func TestIsJVMPod_ByEnv(t *testing.T) {
	for _, envName := range []string{"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JAVA_OPTS"} {
		p := makePod("p", "default", "alpine", map[string]string{envName: "-Xmx512m"})
		if !isJVMPod(*p) {
			t.Errorf("expected JVM pod for env %q", envName)
		}
	}
}

// TestIsPythonPod_ByImage verifies Python detection via image name.
func TestIsPythonPod_ByImage(t *testing.T) {
	p := makePod("p", "default", "python:3.12-slim", nil)
	if !isPythonPod(*p) {
		t.Error("expected Python pod via image")
	}
}

// TestIsPythonPod_ByEnv verifies Python detection via environment variable.
func TestIsPythonPod_ByEnv(t *testing.T) {
	for _, envName := range []string{"PYTHONPATH", "PYTHONUNBUFFERED"} {
		p := makePod("p", "default", "alpine", map[string]string{envName: "1"})
		if !isPythonPod(*p) {
			t.Errorf("expected Python pod for env %q", envName)
		}
	}
}

func TestDetectPodRuntimes_BroadApplicationFamilies(t *testing.T) {
	cases := []struct {
		name    string
		runtime string
		pod     *corev1.Pod
	}{
		{
			name:    "Go image",
			runtime: "go",
			pod:     makePod("checkout-api", "default", "golang:1.23", nil),
		},
		{
			name:    "Go env",
			runtime: "go",
			pod:     makePod("checkout-api", "default", "registry.local/checkout:latest", map[string]string{"GODEBUG": "gctrace=1"}),
		},
		{
			name:    "Node image",
			runtime: "node",
			pod:     makePod("orders-api", "default", "node:22-alpine", nil),
		},
		{
			name:    "Ruby image",
			runtime: "ruby",
			pod:     makePod("billing-api", "default", "ruby:3.3", nil),
		},
		{
			name:    ".NET image",
			runtime: "dotnet",
			pod:     makePod("identity-api", "default", "mcr.microsoft.com/dotnet/aspnet:8.0", nil),
		},
		{
			name:    "native image",
			runtime: "native",
			pod:     makePod("edge-api", "default", "rust:1.78", nil),
		},
		{
			name:    "runtime label",
			runtime: "node",
			pod:     makeLabeledPod("orders-api", "default", "registry.local/orders:latest", map[string]string{"app.kubernetes.io/runtime": "node"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !containsRuntime(detectPodRuntimes(*tc.pod), tc.runtime) {
				t.Errorf("expected %s runtime for %s", tc.runtime, tc.name)
			}
		})
	}
}

// TestPlainInfrastructurePodDoesNotSuggestLanguageRuntime ensures generic
// infrastructure images do not make setup look language-specific.
func TestPlainInfrastructurePodDoesNotSuggestLanguageRuntime(t *testing.T) {
	p := makePod("p", "default", "nginx:latest", nil)
	if isJVMPod(*p) {
		t.Error("nginx should not be JVM")
	}
	if isPythonPod(*p) {
		t.Error("nginx should not be Python")
	}
	if got := detectPodRuntimes(*p); len(got) != 0 {
		t.Errorf("nginx should not suggest an application runtime, got %v", got)
	}
}

func TestDetectPodRuntimes_DoesNotTreatNodeExporterAsNodeJS(t *testing.T) {
	p := makePod("node-exporter", "monitoring", "quay.io/prometheus/node-exporter:v1.9.1", nil)
	if containsRuntime(detectPodRuntimes(*p), "node") {
		t.Error("node-exporter should not be classified as Node.js")
	}
}

func TestIsFrontendPod_ByImageNameLabelAndEnv(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "nginx image",
			pod:  makePod("static-assets", "default", "nginx:1.27", nil),
		},
		{
			name: "next app name",
			pod:  makePod("checkout-next-web", "default", "registry.local/app:20260608", nil),
		},
		{
			name: "component label",
			pod:  makeLabeledPod("checkout", "default", "registry.local/app:20260608", map[string]string{"app.kubernetes.io/component": "frontend"}),
		},
		{
			name: "public build env",
			pod:  makePod("checkout", "default", "registry.local/app:20260608", map[string]string{"NEXT_PUBLIC_API_BASE": "https://api.example.com"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isFrontendPod(*tc.pod) {
				t.Errorf("expected frontend pod for %s", tc.name)
			}
		})
	}
}

func TestIsFrontendPod_DoesNotTreatPlainNodeAPIAsFrontend(t *testing.T) {
	p := makePod("orders-api", "default", "node:22-alpine", map[string]string{"NODE_ENV": "production"})
	if isFrontendPod(*p) {
		t.Error("plain Node API should not be classified as frontend")
	}
}

func TestListPodsForSetupWithPaginatesUntilContinueEmpty(t *testing.T) {
	pages := []*corev1.PodList{
		{
			Items: []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "p1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "p2"}},
			},
			ListMeta: metav1.ListMeta{Continue: "next-page"},
		},
		{
			Items: []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "p3"}},
			},
		},
	}
	var calls []metav1.ListOptions
	got := listPodsForSetupWith(context.Background(), func(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		idx := len(calls)
		calls = append(calls, opts)
		if idx >= len(pages) {
			t.Fatalf("unexpected pod list call %d", idx+1)
			return nil, nil
		}
		return pages[idx], nil
	})

	if len(got.Items) != 3 {
		t.Fatalf("got %d pods, want 3", len(got.Items))
	}
	if got.Incomplete {
		t.Fatal("sample should be complete when final page has no continue token")
	}
	if len(calls) != 2 {
		t.Fatalf("got %d list calls, want 2", len(calls))
	}
	if calls[0].Limit != setupPodScanPageLimit {
		t.Errorf("first call Limit = %d, want %d", calls[0].Limit, setupPodScanPageLimit)
	}
	if calls[0].Continue != "" {
		t.Errorf("first call Continue = %q, want empty", calls[0].Continue)
	}
	if calls[1].Continue != "next-page" {
		t.Errorf("second call Continue = %q, want next-page", calls[1].Continue)
	}
}

func TestListPodsForSetupWithCapsSample(t *testing.T) {
	pages := []*corev1.PodList{
		{Items: makePodItems(setupPodScanMaxPods + 1)},
	}
	got := listPodsForSetupWith(context.Background(), func(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		return pages[0], nil
	})

	if len(got.Items) != setupPodScanMaxPods {
		t.Fatalf("got %d pods, want cap %d", len(got.Items), setupPodScanMaxPods)
	}
	if !got.Incomplete {
		t.Fatal("sample should be incomplete when the cap truncates a page")
	}
	if got.IncompleteReason != setupPodSampleIncompleteCap {
		t.Fatalf("IncompleteReason = %q, want %q", got.IncompleteReason, setupPodSampleIncompleteCap)
	}
}

func TestListPodsForSetupWithMarksIncompleteOnListError(t *testing.T) {
	got := listPodsForSetupWith(context.Background(), func(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		return nil, context.Canceled
	})

	if len(got.Items) != 0 {
		t.Fatalf("got %d pods, want none after list error", len(got.Items))
	}
	if !got.Incomplete {
		t.Fatal("sample should be incomplete when a pod list page fails")
	}
	if got.IncompleteReason != setupPodSampleIncompleteListError {
		t.Fatalf("IncompleteReason = %q, want %q", got.IncompleteReason, setupPodSampleIncompleteListError)
	}
}

func makePodItems(count int) []corev1.Pod {
	items := make([]corev1.Pod, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}})
	}
	return items
}

// TestDetectComponents_MetricsServer verifies metrics-server detection.
func TestDetectComponents_MetricsServer(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-server", Namespace: "kube-system"},
	}
	objs := []runtime.Object{dep}
	core := fake.NewSimpleClientset(objs...)

	result := &ContextResult{}
	detectComponents(context.Background(), core, []string{"kube-system"}, result)

	if !result.HasMetricsServer {
		t.Error("expected HasMetricsServer=true")
	}
}

// TestDetectComponents_Prometheus verifies Prometheus service detection.
func TestDetectComponents_Prometheus(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus-operated",
			Namespace: "monitoring",
			Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
		},
	}
	core := fake.NewSimpleClientset(svc)

	result := &ContextResult{}
	detectComponents(context.Background(), core, []string{"monitoring"}, result)

	if !result.HasPrometheus {
		t.Error("expected HasPrometheus=true")
	}
	if len(result.PrometheusURLs) == 0 {
		t.Error("expected at least one PrometheusURL")
	}
}

func TestDetectComponents_IngressFrontendSurface(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-web", Namespace: "default"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{Host: "checkout.example.com"}},
		},
	}
	core := fake.NewSimpleClientset(ing)

	result := &ContextResult{}
	detectComponents(context.Background(), core, []string{"default"}, result)

	if !result.HasFrontendSurface {
		t.Error("expected HasFrontendSurface=true")
	}
	if result.IngressHostCount != 1 {
		t.Errorf("IngressHostCount = %d, want 1", result.IngressHostCount)
	}
}

// TestDetectComponents_DCGM verifies DCGM DaemonSet detection.
func TestDetectComponents_DCGM(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "dcgm-exporter", Namespace: "gpu-operator"},
	}
	core := fake.NewSimpleClientset(ds)

	result := &ContextResult{}
	detectComponents(context.Background(), core, []string{"gpu-operator"}, result)

	if !result.HasDCGMExporter {
		t.Error("expected HasDCGMExporter=true")
	}
}

// TestProbePermissions_RecordsAllowedVerbs verifies every canonical probe
// is issued and its allowed/denied state reflects the API server response.
func TestProbePermissions_RecordsAllowedVerbs(t *testing.T) {
	core := fake.NewSimpleClientset()
	// Only allow "list pods" and "get pods/log"; everything else denied.
	core.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			ssar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
			attrs := ssar.Spec.ResourceAttributes
			allowed := false
			reason := ""
			switch {
			case attrs.Resource == "pods" && attrs.Subresource == "" && attrs.Verb == "list":
				allowed = true
			case attrs.Resource == "pods" && attrs.Subresource == "log" && attrs.Verb == "get":
				allowed = true
			default:
				reason = "rbac.authorization.k8s.io: forbidden"
			}
			ssar.Status = authorizationv1.SubjectAccessReviewStatus{
				Allowed: allowed,
				Reason:  reason,
			}
			return true, ssar, nil
		},
	)

	got := probePermissions(context.Background(), core)
	if len(got) != len(canonicalPermissionProbes) {
		t.Fatalf("got %d checks, want %d", len(got), len(canonicalPermissionProbes))
	}

	var listPodsSeen, getPodLogSeen, nodesAllowed bool
	for _, c := range got {
		switch {
		case c.Resource == "pods" && c.Subresource == "" && c.Verb == "list":
			listPodsSeen = true
			if !c.Allowed {
				t.Errorf("expected list pods allowed, got denied: %+v", c)
			}
		case c.Resource == "pods" && c.Subresource == "log" && c.Verb == "get":
			getPodLogSeen = true
			if !c.Allowed {
				t.Errorf("expected get pods/log allowed: %+v", c)
			}
		case c.Resource == "nodes" && c.Verb == "list":
			if c.Allowed {
				nodesAllowed = true
			} else if c.Reason == "" {
				t.Errorf("denial without reason: %+v", c)
			}
		}
	}
	if !listPodsSeen {
		t.Error("list pods probe missing from results")
	}
	if !getPodLogSeen {
		t.Error("get pods/log probe missing from results")
	}
	if nodesAllowed {
		t.Error("list nodes should be denied in this fixture")
	}
}

// TestDetectComponents_OTel verifies OpenTelemetry service detection.
func TestDetectComponents_OTel(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "otel-collector", Namespace: "observability"},
	}
	core := fake.NewSimpleClientset(svc)

	result := &ContextResult{}
	detectComponents(context.Background(), core, []string{"observability"}, result)

	if !result.HasOTel {
		t.Error("expected HasOTel=true")
	}
}
