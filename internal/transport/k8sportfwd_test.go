package transport

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// readyPod builds a Running pod with Ready condition True.
func readyPod(ns, name string, lbls map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    lbls,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// svc builds a Service with the given selector (nil selector → headless).
func svc(ns, name string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
		},
	}
}

// TestSelectPod_ReturnsMatchingReadyPod verifies the happy path: a service
// with selector {app: db} and one matching Ready pod → returns that pod's name.
func TestSelectPod_ReturnsMatchingReadyPod(t *testing.T) {
	kube := fake.NewSimpleClientset(
		svc("default", "db", map[string]string{"app": "db"}),
		readyPod("default", "db-0", map[string]string{"app": "db"}),
	)

	got, err := SelectPod(context.Background(), kube, "default", "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "db-0" {
		t.Errorf("want pod name %q, got %q", "db-0", got)
	}
}

// TestSelectPod_NoSelector verifies that a service without a selector returns
// an error describing the absence of a selector.
func TestSelectPod_NoSelector(t *testing.T) {
	kube := fake.NewSimpleClientset(
		svc("default", "headless", nil),
	)

	_, err := SelectPod(context.Background(), kube, "default", "headless")
	if err == nil {
		t.Fatal("expected error for service with no selector, got nil")
	}
}

// TestSelectPod_NoReadyPods verifies that when no pods are in Ready state an
// error is returned even if matching pods exist.
func TestSelectPod_NoReadyPods(t *testing.T) {
	notReady := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-0",
			Namespace: "default",
			Labels:    map[string]string{"app": "db"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}
	kube := fake.NewSimpleClientset(
		svc("default", "db", map[string]string{"app": "db"}),
		notReady,
	)

	_, err := SelectPod(context.Background(), kube, "default", "db")
	if err == nil {
		t.Fatal("expected error when no ready pods exist, got nil")
	}
}
