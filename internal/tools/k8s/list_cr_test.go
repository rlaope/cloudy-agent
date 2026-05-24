package k8s_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// rolloutGVR is the canonical Argo Rollouts GVR — the headline CRD this
// generic reader was built to unlock. Reused across the list_cr tests.
var rolloutGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "rollouts",
}

// newDynHub builds a Hub whose default-context client carries a
// dynamic/fake client seeded with objects, plus stub core/metrics so the
// rest of the Client surface still compiles. The custom GVR-to-ListKind
// map is required because fake.NewSimpleDynamicClient cannot guess list
// kinds for CRDs that are not registered in any runtime.Scheme.
func newDynHub(t *testing.T, gvrToListKind map[schema.GroupVersionResource]string, objs ...runtime.Object) *k8stool.Hub {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	c := k8stool.NewTestClientWithDyn(corefake.NewSimpleClientset(), metricsfake.NewSimpleClientset(), dyn)
	return k8stool.NewHubFromClients(map[string]*k8stool.Client{"": c}, "")
}

func newRollout(ns, name, phase string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
			},
			"spec": map[string]any{
				"replicas": replicas,
			},
			"status": map[string]any{
				"phase": phase,
			},
		},
	}
}

// TestListCR_DefaultFieldsProjectArgoRollout is the headline contract:
// pointed at a fake Argo Rollout, the default field projection
// (metadata.name / metadata.namespace / status.phase) renders exactly
// those values as columns. This is the SRE's first
// "what does this CRD look like?" experience and the regression
// guarantees the dotted-path lookup is wired correctly.
func TestListCR_DefaultFieldsProjectArgoRollout(t *testing.T) {
	h := newDynHub(t,
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		newRollout("prod", "checkout", "Healthy", 3),
	)
	tool := k8stool.NewListCRTool(h)

	args, _ := json.Marshal(map[string]any{
		"group":     "argoproj.io",
		"version":   "v1alpha1",
		"resource":  "rollouts",
		"namespace": "prod",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("list_cr.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row; got: %#v", obs.Table)
	}
	row := obs.Table.Rows[0]
	// Default projection is [metadata.name, metadata.namespace, status.phase].
	wantCols := []string{"checkout", "prod", "Healthy"}
	for i, want := range wantCols {
		if row[i] != want {
			t.Errorf("row[%d] = %q, want %q", i, row[i], want)
		}
	}
}

// TestListCR_CustomFieldProjection exercises the explicit fields argument:
// the agent should be able to ask for any dotted path that exists in the
// Unstructured object. spec.replicas is the workhorse case for any
// workload-shaped CRD (Rollout, ScaledObject, etc.).
func TestListCR_CustomFieldProjection(t *testing.T) {
	h := newDynHub(t,
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		newRollout("prod", "checkout", "Healthy", 5),
	)
	tool := k8stool.NewListCRTool(h)

	args, _ := json.Marshal(map[string]any{
		"group":    "argoproj.io",
		"version":  "v1alpha1",
		"resource": "rollouts",
		"fields":   []string{"metadata.name", "spec.replicas"},
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("list_cr.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row; got: %#v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "checkout" {
		t.Errorf("row[0] = %q, want %q", row[0], "checkout")
	}
	// spec.replicas is an int64 in our seed — projectField must render it
	// as the bare integer string, not a float-with-decimal.
	if row[1] != "5" {
		t.Errorf("row[1] = %q, want %q (replicas should render as integer)", row[1], "5")
	}
}

// TestListCR_MissingFieldsRenderEmpty pins the projection contract: a path
// that does not exist on a row collapses to an empty cell instead of
// aborting the whole table. CRDs are heterogeneous and the agent will
// frequently ask for fields some items do not have.
func TestListCR_MissingFieldsRenderEmpty(t *testing.T) {
	// One rollout has no status.phase — the canonical "field missing" case.
	noPhase := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata": map[string]any{
				"name":      "no-phase",
				"namespace": "prod",
			},
		},
	}
	h := newDynHub(t,
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		noPhase,
	)
	tool := k8stool.NewListCRTool(h)

	args, _ := json.Marshal(map[string]any{
		"group":    "argoproj.io",
		"version":  "v1alpha1",
		"resource": "rollouts",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("list_cr.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row; got: %#v", obs.Table)
	}
	// Default projection: [name, namespace, status.phase] — status.phase
	// must render as empty, not break the row.
	row := obs.Table.Rows[0]
	if row[0] != "no-phase" {
		t.Errorf("row[0] = %q, want no-phase", row[0])
	}
	if row[2] != "" {
		t.Errorf("row[2] = %q, want empty (missing status.phase)", row[2])
	}
}

// TestListCR_RequiredArgsValidated guards the GVR-required contract:
// omitting any of group/version/resource must surface a Go error rather
// than dispatch a malformed dynamic-client call.
func TestListCR_RequiredArgsValidated(t *testing.T) {
	h := newDynHub(t, nil)
	tool := k8stool.NewListCRTool(h)

	args, _ := json.Marshal(map[string]any{
		"group":   "argoproj.io",
		"version": "v1alpha1",
		// resource missing
	})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for missing resource arg, got nil")
	}
	if !strings.Contains(err.Error(), "group, version, resource are required") {
		t.Errorf("error should name the missing-args contract; got: %v", err)
	}
}
