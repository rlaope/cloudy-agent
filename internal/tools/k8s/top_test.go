package k8s_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// newHubMetricsErr builds a single-context Hub whose metrics clientset
// returns the given error for any list against nodemetricses / podmetricses,
// matching what an apiserver without metrics-server returns
// ("the server could not find the requested resource …").
func newHubMetricsErr(t *testing.T, listErr error) *k8sclient.Hub {
	t.Helper()
	fm := fakemetrics.NewSimpleClientset()
	react := func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, listErr
	}
	fm.PrependReactor("list", "nodes", react)
	fm.PrependReactor("list", "pods", react)
	client := k8sclient.NewTestClient(fake.NewSimpleClientset(), fm)
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": client}, "")
}

// TestTopNodes_MetricsUnavailable verifies that a metrics-server outage is
// surfaced as an actionable Observation (not a tool error), so the LLM relays
// the explanation to the user verbatim instead of the bare wrapped error.
func TestTopNodes_MetricsUnavailable(t *testing.T) {
	apiErr := errors.New("the server could not find the requested resource (get nodes.metrics.k8s.io)")
	hub := newHubMetricsErr(t, apiErr)

	tool := k8stool.NewTopNodesTool(hub)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("expected nil error (metrics-unavailable is soft-handled), got: %v", err)
	}
	for _, want := range []string{
		"metrics-server is not available",
		"kubectl",
		"kube-system metrics-server",
		"v1beta1.metrics.k8s.io",
		"github.com/kubernetes-sigs/metrics-server",
		apiErr.Error(),
	} {
		if !strings.Contains(obs.Text, want) {
			t.Errorf("Observation.Text missing %q:\n%s", want, obs.Text)
		}
	}
}

// TestTopPods_MetricsUnavailable covers the same path for the pod variant.
func TestTopPods_MetricsUnavailable(t *testing.T) {
	apiErr := errors.New("the server could not find the requested resource (get pods.metrics.k8s.io)")
	hub := newHubMetricsErr(t, apiErr)

	tool := k8stool.NewTopPodsTool(hub)
	args, _ := json.Marshal(map[string]any{"namespace": "default"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !strings.Contains(obs.Text, "metrics-server is not available") {
		t.Errorf("Observation.Text missing actionable message:\n%s", obs.Text)
	}
}

// TestTopNodes_OK keeps the happy path covered so the new soft-error branch
// in list_resource cannot regress the success contract. The seeded NodeMetrics
// has a real Name and non-zero Usage so we can assert row projection — not
// just that a Table envelope was allocated.
func TestTopNodes_OK(t *testing.T) {
	// fakemetrics.NewSimpleClientset's tracker drops seeded NodeMetrics in
	// some configurations, so install a reactor that synthesizes a non-empty
	// NodeMetricsList directly — that's what the apiserver would return.
	fm := fakemetrics.NewSimpleClientset()
	fm.PrependReactor("list", "nodes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.NodeMetricsList{
			Items: []metricsv1beta1.NodeMetrics{{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			}},
		}, nil
	})
	client := k8sclient.NewTestClient(fake.NewSimpleClientset(), fm)
	hub := k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": client}, "")

	tool := k8stool.NewTopNodesTool(hub)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected one projected row, got: %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "node-1" {
		t.Errorf("row[0] (name) = %q, want %q", row[0], "node-1")
	}
	if row[1] != "250" {
		t.Errorf("row[1] (cpu millis) = %q, want %q", row[1], "250")
	}
	if row[2] != "512" {
		t.Errorf("row[2] (memory Mi) = %q, want %q", row[2], "512")
	}
}

// TestTopNodes_MetricsUnavailable_NamedContext exercises the ctxName != ""
// branch of metricsUnavailableMessage. The empty-ctx branch is covered by
// TestTopNodes_MetricsUnavailable; without this companion the `--context %q`
// rendering (and its trailing-space contract) would be untested.
func TestTopNodes_MetricsUnavailable_NamedContext(t *testing.T) {
	apiErr := errors.New("the server could not find the requested resource (get nodes.metrics.k8s.io)")
	fm := fakemetrics.NewSimpleClientset()
	react := func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiErr
	}
	fm.PrependReactor("list", "nodes", react)
	client := k8sclient.NewTestClient(fake.NewSimpleClientset(), fm)
	hub := k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"prod": client}, "prod")

	tool := k8stool.NewTopNodesTool(hub)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	for _, want := range []string{
		`context "prod"`,
		`--context "prod"`,
	} {
		if !strings.Contains(obs.Text, want) {
			t.Errorf("Observation.Text missing %q:\n%s", want, obs.Text)
		}
	}
}

// TestListPods_NonMetricsError_HardPath pins the contract that a non-
// k8sclient.ErrMetricsUnavailable error from any ListResourceSpec.Items still falls
// through the hard-error wrap (`<tool>: <err>`), so a future refactor that
// widens the soft branch (e.g. to `if err != nil`) is caught.
func TestListPods_NonMetricsError_HardPath(t *testing.T) {
	fakeCore := fake.NewSimpleClientset()
	rbacErr := errors.New("pods is forbidden: User cannot list resource pods")
	fakeCore.PrependReactor("list", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, rbacErr
	})
	client := k8sclient.NewTestClient(fakeCore, fakemetrics.NewSimpleClientset())
	hub := k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": client}, "")

	tool := k8stool.NewListPodsTool(hub)
	args, _ := json.Marshal(map[string]any{"namespace": "default"})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected non-metrics error to bubble as a hard error, got nil")
	}
	for _, want := range []string{"k8s.list_pods", rbacErr.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q:\n%v", want, err)
		}
	}
}
