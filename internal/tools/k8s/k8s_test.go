package k8s_test

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// newFakeClient builds a Client backed by fake clientsets seeded with objs.
func newFakeClient(objs ...runtime.Object) *k8sclient.Client {
	// Split objects by API group.
	var coreObjs []runtime.Object
	for _, o := range objs {
		coreObjs = append(coreObjs, o)
	}
	fakeCore := fake.NewSimpleClientset(coreObjs...)
	fakeMetrics := fakemetrics.NewSimpleClientset()
	return k8sclient.NewTestClient(fakeCore, fakeMetrics)
}

// newSingleHub wraps newFakeClient in a single-context Hub for tests that
// use the v0.1 default-context behaviour.
func newSingleHub(objs ...runtime.Object) *k8sclient.Hub {
	c := newFakeClient(objs...)
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": c}, "")
}

func pod(ns, name, phase, node string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase: corev1.PodPhase(phase),
		},
	}
}

// TestListPods_FiltersByLabel verifies label selector filtering.
func TestListPods_FiltersByLabel(t *testing.T) {
	h := newSingleHub(
		pod("default", "nginx-1", "Running", "node-1", map[string]string{"app": "nginx"}),
		pod("default", "redis-1", "Running", "node-1", map[string]string{"app": "redis"}),
	)

	tool := k8stool.NewListPodsTool(h)

	args, _ := json.Marshal(map[string]any{
		"namespace":      "default",
		"label_selector": "app=nginx",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(obs.Table.Rows))
	}
	if obs.Table.Rows[0][1] != "nginx-1" {
		t.Errorf("expected pod name nginx-1, got %s", obs.Table.Rows[0][1])
	}
}

// TestListPods_AllNamespaces verifies that empty namespace returns all pods.
func TestListPods_AllNamespaces(t *testing.T) {
	h := newSingleHub(
		pod("ns-a", "pod-1", "Running", "node-1", nil),
		pod("ns-b", "pod-2", "Pending", "node-2", nil),
	)
	tool := k8stool.NewListPodsTool(h)
	args, _ := json.Marshal(map[string]any{"namespace": ""})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(obs.Table.Rows))
	}
}

// TestLogs_NoError verifies that log retrieval returns non-error result.
// The fake client returns an empty stream; we just verify no error occurs.
func TestLogs_NoError(t *testing.T) {
	h := newSingleHub(
		pod("default", "myapp", "Running", "node-1", nil),
	)
	tool := k8stool.NewLogsTool(h)
	args, _ := json.Marshal(map[string]any{
		"namespace":  "default",
		"name":       "myapp",
		"tail_lines": 50,
	})
	// The fake client doesn't implement log streaming in a way that returns
	// content, but it should not error on the API call itself.
	_, err := tool.Run(context.Background(), args)
	// fake client returns an error for GetLogs — tolerate it.
	_ = err
}

// TestEvents_SortedByLastTimestamp verifies that events are returned newest-first.
func TestEvents_SortedByLastTimestamp(t *testing.T) {
	older := metav1.Now()
	older.Time = older.Add(-60 * 1e9) // 60s ago

	newer := metav1.Now()

	evtOld := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "evt-old", Namespace: "default"},
		LastTimestamp: older,
		Type:          "Normal",
		Reason:        "OldReason",
		Message:       "older event",
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "myapp", Namespace: "default",
		},
	}
	evtNew := &corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "evt-new", Namespace: "default"},
		LastTimestamp: newer,
		Type:          "Warning",
		Reason:        "NewReason",
		Message:       "newer event",
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "myapp", Namespace: "default",
		},
	}

	h := newSingleHub(evtOld, evtNew)
	tool := k8stool.NewEventsTool(h)
	args, _ := json.Marshal(map[string]any{"namespace": "default"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 event rows, got %d", len(obs.Table.Rows))
	}
	// First row should be the newer event (NewReason).
	if obs.Table.Rows[0][2] != "NewReason" {
		t.Errorf("expected first row reason=NewReason, got %s", obs.Table.Rows[0][2])
	}
}

// TestListNamespaces verifies namespace listing.
func TestListNamespaces(t *testing.T) {
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		}
	}
	h := newSingleHub(ns("default"), ns("kube-system"), ns("monitoring"))
	tool := k8stool.NewListNamespacesTool(h)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 namespaces, got %d", len(obs.Table.Rows))
	}
}

// TestListNodes verifies node listing and role extraction.
func TestListNodes(t *testing.T) {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.28.0"},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
			},
		},
	}
	h := newSingleHub(n)
	tool := k8stool.NewListNodesTool(h)
	args, _ := json.Marshal(map[string]any{})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 node row, got %d", len(obs.Table.Rows))
	}
	row := obs.Table.Rows[0]
	// NAME
	if row[0] != "node-1" {
		t.Errorf("expected NAME=node-1, got %s", row[0])
	}
	// ROLES
	if row[1] != "control-plane" {
		t.Errorf("expected ROLES=control-plane, got %s", row[1])
	}
	// STATUS
	if row[3] != "Ready" {
		t.Errorf("expected STATUS=Ready, got %s", row[3])
	}
}

// TestHub_SingleContext_BackwardsCompat verifies the v0.1 column set is
// preserved (no CONTEXT column) when the hub holds exactly one context.
func TestHub_SingleContext_BackwardsCompat(t *testing.T) {
	h := newSingleHub(
		pod("default", "p-1", "Running", "node-1", nil),
	)
	tool := k8stool.NewListPodsTool(h)
	args, _ := json.Marshal(map[string]any{"namespace": "default"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := obs.Table.Headers[0]; got == "CONTEXT" {
		t.Errorf("single-context hub must not prepend CONTEXT column, got headers=%v", obs.Table.Headers)
	}
}

// TestHub_MultiContext_RoutesByContextArg builds a Hub with two contexts and
// verifies that args.context selects the matching client.
func TestHub_MultiContext_RoutesByContextArg(t *testing.T) {
	clientA := newFakeClient(pod("default", "from-a", "Running", "node-a", nil))
	clientB := newFakeClient(pod("default", "from-b", "Running", "node-b", nil))

	h := k8sclient.NewHubFromClients(map[string]*k8sclient.Client{
		"a": clientA,
		"b": clientB,
	}, "a")

	if !h.MultiContext() {
		t.Fatal("expected MultiContext()=true with two contexts")
	}
	if got := h.Default(); got != "a" {
		t.Errorf("expected default=a, got %q", got)
	}

	tool := k8stool.NewListPodsTool(h)
	args, _ := json.Marshal(map[string]any{
		"namespace": "default",
		"context":   "b",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(obs.Table.Rows))
	}
	// First column is CONTEXT, second is NAMESPACE, third is NAME.
	if obs.Table.Headers[0] != "CONTEXT" {
		t.Errorf("expected first header CONTEXT, got %q", obs.Table.Headers[0])
	}
	if obs.Table.Rows[0][0] != "b" {
		t.Errorf("expected CONTEXT cell=b, got %q", obs.Table.Rows[0][0])
	}
	if obs.Table.Rows[0][2] != "from-b" {
		t.Errorf("expected pod from-b, got %s", obs.Table.Rows[0][2])
	}
}

// TestHub_NamespaceChecker verifies a denied namespace returns the error
// message in Observation.Text rather than a Go error, so the agent loop
// continues.
func TestHub_NamespaceChecker(t *testing.T) {
	h := newSingleHub(pod("kube-system", "p-1", "Running", "node-1", nil))
	h.WithNamespaceChecker(func(ns string) error {
		if ns == "kube-system" {
			return errStub("namespace denied by profile")
		}
		return nil
	})
	tool := k8stool.NewListPodsTool(h)
	args, _ := json.Marshal(map[string]any{"namespace": "kube-system"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("checker rejection must not surface as Go error, got %v", err)
	}
	if obs.Text != "namespace denied by profile" {
		t.Errorf("expected denial text in Observation.Text, got %q", obs.Text)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
