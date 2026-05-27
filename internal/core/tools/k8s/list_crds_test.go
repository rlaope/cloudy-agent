package k8s_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	k8stool "github.com/rlaope/cloudy/internal/core/tools/k8s"
)

// crdListGVR is the dynamic-client GVR for installed CRDs themselves —
// mirrors the production constant in list_crds.go. Tests cannot import an
// unexported package variable so the duplication is the smallest surface
// we can carry (and a deliberate signal: if the production GVR moves, the
// test should fail loudly and the fix is one line in both places).
var crdListGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// newCRD builds a minimal unstructured representation of a CRD matching the
// fields the tool projects. Group, scope, kind, plural, and the Established
// status condition cover every column the table renders.
func newCRD(name, group, scope, kind, plural string, versions []string, established string) *unstructured.Unstructured {
	versionObjs := make([]any, 0, len(versions))
	for _, v := range versions {
		versionObjs = append(versionObjs, map[string]any{
			"name":    v,
			"served":  true,
			"storage": v == versions[0],
		})
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": name,
			},
			"spec": map[string]any{
				"group": group,
				"scope": scope,
				"names": map[string]any{
					"kind":   kind,
					"plural": plural,
				},
				"versions": versionObjs,
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":   "Established",
						"status": established,
					},
				},
			},
		},
	}
}

// TestListCRDs_ProjectsExpectedColumns is the headline contract: with two
// fake CRDs seeded, the tool renders the NAME / GROUP / VERSIONS / SCOPE /
// KIND / PLURAL / ESTABLISHED columns the schema advertises.
func TestListCRDs_ProjectsExpectedColumns(t *testing.T) {
	rolloutCRD := newCRD("rollouts.argoproj.io", "argoproj.io", "Namespaced", "Rollout", "rollouts",
		[]string{"v1alpha1"}, "True")
	gatewayCRD := newCRD("gateways.gateway.networking.k8s.io", "gateway.networking.k8s.io", "Namespaced",
		"Gateway", "gateways", []string{"v1", "v1beta1"}, "True")

	h := newDynHub(t,
		map[schema.GroupVersionResource]string{
			crdListGVR: "CustomResourceDefinitionList",
		},
		rolloutCRD, gatewayCRD,
	)
	tool := k8stool.NewListCRDsTool(h)

	obs, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_crds.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows; got: %#v", obs.Table)
	}

	all := strings.Join([]string{
		strings.Join(obs.Table.Rows[0], " "),
		strings.Join(obs.Table.Rows[1], " "),
	}, "\n")
	for _, want := range []string{
		"rollouts.argoproj.io", "argoproj.io", "v1alpha1", "Namespaced", "Rollout", "rollouts",
		"gateways.gateway.networking.k8s.io", "gateway.networking.k8s.io", "v1,v1beta1",
		"Gateway", "gateways",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("CRD output missing %q; output=%q", want, all)
		}
	}
}

// TestListCRDs_GroupFilter pins the `group` arg contract: filtering by
// exact group name narrows the result set. SREs run this to ask
// "which Argo CRDs are installed?" without first knowing every name.
func TestListCRDs_GroupFilter(t *testing.T) {
	argo := newCRD("rollouts.argoproj.io", "argoproj.io", "Namespaced", "Rollout", "rollouts",
		[]string{"v1alpha1"}, "True")
	keda := newCRD("scaledobjects.keda.sh", "keda.sh", "Namespaced", "ScaledObject", "scaledobjects",
		[]string{"v1alpha1"}, "True")
	gw := newCRD("gateways.gateway.networking.k8s.io", "gateway.networking.k8s.io", "Namespaced",
		"Gateway", "gateways", []string{"v1"}, "True")

	h := newDynHub(t,
		map[schema.GroupVersionResource]string{
			crdListGVR: "CustomResourceDefinitionList",
		},
		argo, keda, gw,
	)
	tool := k8stool.NewListCRDsTool(h)

	args, _ := json.Marshal(map[string]any{"group": "argoproj.io"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("list_crds.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("group filter should narrow to 1 row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	if !strings.Contains(row, "rollouts.argoproj.io") {
		t.Errorf("filtered row should be the argoproj CRD; got: %q", row)
	}
}

// TestListCRDs_NameContainsFilter exercises the substring filter — the
// quickest way for the agent to find a CRD when it knows the kind but
// not the full group.
func TestListCRDs_NameContainsFilter(t *testing.T) {
	rolloutCRD := newCRD("rollouts.argoproj.io", "argoproj.io", "Namespaced", "Rollout", "rollouts",
		[]string{"v1alpha1"}, "True")
	appCRD := newCRD("applications.argoproj.io", "argoproj.io", "Namespaced", "Application", "applications",
		[]string{"v1alpha1"}, "True")

	h := newDynHub(t,
		map[schema.GroupVersionResource]string{
			crdListGVR: "CustomResourceDefinitionList",
		},
		rolloutCRD, appCRD,
	)
	tool := k8stool.NewListCRDsTool(h)

	args, _ := json.Marshal(map[string]any{"name_contains": "rollout"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("list_crds.Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("name_contains filter should narrow to 1 row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	if !strings.Contains(row, "rollouts.argoproj.io") {
		t.Errorf("filtered row should be the rollouts CRD; got: %q", row)
	}
}
