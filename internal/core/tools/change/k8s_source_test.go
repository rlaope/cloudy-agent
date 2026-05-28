package change

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
)

func ts(base time.Time, h int) metav1.Time {
	return metav1.NewTime(base.Add(time.Duration(h) * time.Hour))
}

func ownerRef(kind, name string) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: kind, Name: name}
}

func rs(name, dep, image string, revision string, created metav1.Time) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "prod",
			CreationTimestamp: created,
			Annotations:       map[string]string{deploymentRevisionAnnotation: revision},
			OwnerReferences:   []metav1.OwnerReference{ownerRef("Deployment", dep)},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

// TestDeploymentRevisionEvents_OrderingAndImageDiff pins the headline contract:
// each owned ReplicaSet becomes one "image" revision, ordered newest-first,
// with Before/After chaining the prior revision's image into the next.
func TestDeploymentRevisionEvents_OrderingAndImageDiff(t *testing.T) {
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"}}
	list := &appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{
		rs("api-1", "api", "api:v1", "1", ts(base, 0)),
		rs("api-2", "api", "api:v2", "2", ts(base, 1)),
		// Owned by a different deployment — must be ignored.
		rs("other-1", "worker", "worker:v9", "1", ts(base, 2)),
	}}

	got := deploymentRevisionEvents(dep, list)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (only api-owned RS); got %#v", len(got), got)
	}
	// MergeSorted is what orders newest-first; the helper itself emits
	// oldest-first so Before chains correctly. Assert the chain here.
	if got[0].After != "api:v1" || got[0].Before != "" {
		t.Errorf("rev 1: Before=%q After=%q, want \"\"/\"api:v1\"", got[0].Before, got[0].After)
	}
	if got[1].After != "api:v2" || got[1].Before != "api:v1" {
		t.Errorf("rev 2: Before=%q After=%q, want \"api:v1\"/\"api:v2\"", got[1].Before, got[1].After)
	}
	for _, e := range got {
		if e.Kind != "image" || e.Source != "k8s" || e.Target != "api" {
			t.Errorf("event Kind/Source/Target = %q/%q/%q, want image/k8s/api", e.Kind, e.Source, e.Target)
		}
	}
}

// TestControllerRevisionEvents_OwnerFilter verifies StatefulSet/DaemonSet
// revisions are matched by owner name (kind-agnostic) and carry the revision
// number; non-owned revisions are dropped.
func TestControllerRevisionEvents_OwnerFilter(t *testing.T) {
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	list := &appsv1.ControllerRevisionList{Items: []appsv1.ControllerRevision{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kafka-1", CreationTimestamp: ts(base, 0),
				OwnerReferences: []metav1.OwnerReference{ownerRef("StatefulSet", "kafka")},
			},
			Revision: 1,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kafka-2", CreationTimestamp: ts(base, 1),
				OwnerReferences: []metav1.OwnerReference{ownerRef("StatefulSet", "kafka")},
			},
			Revision: 2,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis-1", CreationTimestamp: ts(base, 2),
				OwnerReferences: []metav1.OwnerReference{ownerRef("StatefulSet", "redis")},
			},
			Revision: 1,
		},
	}}

	got := controllerRevisionEvents("kafka", list)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (kafka-owned only)", len(got))
	}
	for _, e := range got {
		if e.Kind != "rollout" || e.Target != "kafka" || e.Source != "k8s" {
			t.Errorf("event = %#v, want rollout/kafka/k8s", e)
		}
	}
}

// TestEventEvents_ReasonMessageAndTime confirms the event reason+message is
// folded into Summary and the last-seen timestamp is used.
func TestEventEvents_ReasonMessageAndTime(t *testing.T) {
	last := metav1.NewTime(time.Date(2026, 5, 28, 11, 30, 0, 0, time.UTC))
	evs := &corev1.EventList{Items: []corev1.Event{
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "api"},
			Reason:         "ScalingReplicaSet",
			Message:        "Scaled up replica set api-2 to 3",
			LastTimestamp:  last,
		},
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "other"},
			Reason:         "Nope",
			Message:        "unrelated",
			LastTimestamp:  last,
		},
	}}

	got := eventEvents("api", evs)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only api-involved)", len(got))
	}
	if got[0].Kind != "event" || got[0].Summary != "ScalingReplicaSet: Scaled up replica set api-2 to 3" {
		t.Errorf("event = %#v", got[0])
	}
	if !got[0].Time.Equal(last.Time) {
		t.Errorf("Time = %v, want %v", got[0].Time, last.Time)
	}
}

// TestHPAEvents_TargetFilter verifies only HPAs whose scaleTargetRef matches
// the workload contribute a scale event carrying the min/max/current bounds.
func TestHPAEvents_TargetFilter(t *testing.T) {
	min := int32(2)
	hpas := &autoscalingv2.HorizontalPodAutoscalerList{Items: []autoscalingv2.HorizontalPodAutoscaler{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"},
				MinReplicas:    &min,
				MaxReplicas:    10,
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 4},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "other-hpa", Namespace: "prod"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "other"},
				MaxReplicas:    5,
			},
		},
	}}

	got := hpaEvents("api", hpas)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only api-targeting HPA)", len(got))
	}
	if got[0].Kind != "scale" || got[0].After != "4" {
		t.Errorf("event = %#v, want scale/After=4", got[0])
	}
}

// TestSpecBaselineEvents_NilReplicasDefaultsToOne pins the Kubernetes default:
// a nil .Spec.Replicas means one replica, and the image baseline is emitted
// only when an image is present.
func TestSpecBaselineEvents_NilReplicasDefaultsToOne(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			// Replicas left nil.
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "api:v2"}}},
			},
		},
	}
	got := deploymentSpecEvents(dep)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (image + scale)", len(got))
	}
	var scale *ChangeEvent
	for i := range got {
		if got[i].Kind == "scale" {
			scale = &got[i]
		}
	}
	if scale == nil || scale.After != "1" {
		t.Errorf("scale baseline = %#v, want After=1", scale)
	}
}

// TestFilterSince_DropsOlderThanCutoff verifies the Since window drops events
// strictly older than the cutoff and keeps events at or after it.
func TestFilterSince_DropsOlderThanCutoff(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	cutoff := base.Add(-30 * time.Minute)
	events := []ChangeEvent{
		{Time: base, Kind: "image"},
		{Time: base.Add(-1 * time.Hour), Kind: "scale"}, // older than cutoff
		{Time: cutoff, Kind: "event"},                   // exactly at cutoff — kept
	}
	got := filterSince(events, cutoff)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (drop the 1h-old one)", len(got))
	}
	for _, e := range got {
		if e.Kind == "scale" {
			t.Errorf("event older than cutoff was not dropped: %#v", e)
		}
	}
}

// TestRecentChanges_DeploymentViaFakeHub exercises the full RecentChanges path
// against a fake-backed Hub (no kubeconfig). It confirms the source resolves
// the client, merges revision + spec + event groups newest-first, and honours
// the limit.
func TestRecentChanges_DeploymentViaFakeHub(t *testing.T) {
	base := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	replicas := int32(3)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: ts(base, 5)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "api:v2"}}},
			},
		},
	}
	rs1 := rs("api-1", "api", "api:v1", "1", ts(base, 0))
	rs2 := rs("api-2", "api", "api:v2", "2", ts(base, 4))
	evt := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "prod"},
		InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "api"},
		Reason:         "ScalingReplicaSet",
		Message:        "Scaled up",
		LastTimestamp:  ts(base, 4),
	}

	hub := newChangeHub(dep, &rs1, &rs2, evt)
	src := NewK8sSource(hub)
	if src.Name() != "k8s" {
		t.Fatalf("Name() = %q, want k8s", src.Name())
	}

	got, err := src.RecentChanges(context.Background(), ChangeQuery{Workload: "api", Namespace: "prod"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("RecentChanges returned no events")
	}
	// Newest-first: the first event's time must be >= every other event's time.
	for i := 1; i < len(got); i++ {
		if got[i].Time.After(got[0].Time) {
			t.Errorf("not sorted newest-first: got[%d].Time %v after got[0].Time %v", i, got[i].Time, got[0].Time)
		}
	}

	// Limit is honoured.
	lim, err := src.RecentChanges(context.Background(), ChangeQuery{Workload: "api", Namespace: "prod", Limit: 2})
	if err != nil {
		t.Fatalf("RecentChanges(limit): %v", err)
	}
	if len(lim) != 2 {
		t.Errorf("limit=2 returned %d events", len(lim))
	}
}

// newChangeHub builds a single-context Hub backed by a fake clientset seeded
// with objs — hermetic, no kubeconfig required.
func newChangeHub(objs ...runtime.Object) *k8sclient.Hub {
	fakeCore := fake.NewSimpleClientset(objs...)
	fakeMetrics := fakemetrics.NewSimpleClientset()
	c := k8sclient.NewTestClient(fakeCore, fakeMetrics)
	return k8sclient.NewHubFromClients(map[string]*k8sclient.Client{"": c}, "")
}
