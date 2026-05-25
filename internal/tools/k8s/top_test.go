package k8s_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// newHubMetricsErr builds a single-context Hub whose metrics clientset
// returns the given error for any list against nodemetricses / podmetricses,
// matching what an apiserver without metrics-server returns
// ("the server could not find the requested resource …").
func newHubMetricsErr(t *testing.T, listErr error) *k8stool.Hub {
	t.Helper()
	fm := fakemetrics.NewSimpleClientset()
	react := func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, listErr
	}
	fm.PrependReactor("list", "nodes", react)
	fm.PrependReactor("list", "pods", react)
	client := k8stool.NewTestClient(fake.NewSimpleClientset(), fm)
	return k8stool.NewHubFromClients(map[string]*k8stool.Client{"": client}, "")
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
// in list_resource cannot regress the success contract.
func TestTopNodes_OK(t *testing.T) {
	fm := fakemetrics.NewSimpleClientset(
		&metricsv1beta1.NodeMetrics{},
	)
	client := k8stool.NewTestClient(fake.NewSimpleClientset(), fm)
	hub := k8stool.NewHubFromClients(map[string]*k8stool.Client{"": client}, "")

	tool := k8stool.NewTopNodesTool(hub)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected a Table on success")
	}
}
