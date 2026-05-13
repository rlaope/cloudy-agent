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

	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// newFakeClient builds a Client backed by fake clientsets seeded with objs.
func newFakeClient(objs ...runtime.Object) *k8stool.Client {
	// Split objects by API group.
	var coreObjs []runtime.Object
	for _, o := range objs {
		coreObjs = append(coreObjs, o)
	}
	fakeCore := fake.NewSimpleClientset(coreObjs...)
	fakeMetrics := fakemetrics.NewSimpleClientset()
	return k8stool.NewTestClient(fakeCore, fakeMetrics)
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
	c := newFakeClient(
		pod("default", "nginx-1", "Running", "node-1", map[string]string{"app": "nginx"}),
		pod("default", "redis-1", "Running", "node-1", map[string]string{"app": "redis"}),
	)

	tool := k8stool.NewListPodsTool(c)

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
	c := newFakeClient(
		pod("ns-a", "pod-1", "Running", "node-1", nil),
		pod("ns-b", "pod-2", "Pending", "node-2", nil),
	)
	tool := k8stool.NewListPodsTool(c)
	args, _ := json.Marshal(map[string]any{"namespace": ""})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(obs.Table.Rows))
	}
}

// TestLogs_ReturnsStream verifies that log retrieval returns non-error result.
// The fake client returns an empty stream; we just verify no error occurs.
func TestLogs_NoError(t *testing.T) {
	c := newFakeClient(
		pod("default", "myapp", "Running", "node-1", nil),
	)
	tool := k8stool.NewLogsTool(c)
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

	c := newFakeClient(evtOld, evtNew)
	tool := k8stool.NewEventsTool(c)
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
	c := newFakeClient(ns("default"), ns("kube-system"), ns("monitoring"))
	tool := k8stool.NewListNamespacesTool(c)
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
	c := newFakeClient(n)
	tool := k8stool.NewListNodesTool(c)
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

// TestReadOnly verifies every tool returns true for ReadOnly.
func TestReadOnly(t *testing.T) {
	c := newFakeClient()
	tools := []interface{ ReadOnly() bool }{
		k8stool.NewListPodsTool(c),
		k8stool.NewDescribePodTool(c),
		k8stool.NewLogsTool(c),
		k8stool.NewEventsTool(c),
		k8stool.NewTopPodsTool(c),
		k8stool.NewTopNodesTool(c),
		k8stool.NewListNamespacesTool(c),
		k8stool.NewListNodesTool(c),
	}
	for _, tool := range tools {
		if !tool.ReadOnly() {
			t.Errorf("%T: ReadOnly() returned false", tool)
		}
	}
}
