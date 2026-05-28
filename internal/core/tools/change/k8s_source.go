package change

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
)

// deploymentRevisionAnnotation is the annotation Kubernetes stamps on each
// ReplicaSet a Deployment owns, recording that ReplicaSet's revision number.
// We read it to order revisions and label rollout events.
const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// K8sSource is a read-only ChangeSource backed by the Kubernetes API. It
// reconstructs recent rollout / image / scale changes for a single workload
// from ReplicaSet and ControllerRevision history plus the workload's current
// spec, and folds in matching cluster Events. Nothing here mutates the cluster.
type K8sSource struct {
	hub *k8sclient.Hub
}

// NewK8sSource builds a K8sSource over the given multi-context Hub.
func NewK8sSource(hub *k8sclient.Hub) *K8sSource {
	return &K8sSource{hub: hub}
}

// Name identifies this backend for diagnostics and logging.
func (s *K8sSource) Name() string { return "k8s" }

// RecentChanges resolves the client for q.Context from the Hub (mirroring the
// k8s.* tools), then derives ChangeEvents from the workload's revision history,
// current spec, autoscaler, and Events. The workload kind is unknown to us, so
// we attempt Deployment, StatefulSet, and DaemonSet in turn and collect what
// each yields — a NotFound for one kind is not fatal. Results are merged
// newest-first and capped by q.Limit.
func (s *K8sSource) RecentChanges(ctx context.Context, q ChangeQuery) ([]ChangeEvent, error) {
	client, err := s.hub.Get(q.Context)
	if err != nil {
		return nil, fmt.Errorf("change.k8s: %w", err)
	}

	var cutoff time.Time
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since)
	}

	var groups [][]ChangeEvent

	// Deployment: revision history lives in owned ReplicaSets.
	if deps, err := client.Deployments(q.Namespace, metav1.ListOptions{}); err == nil {
		if dep := findDeployment(deps, q.Workload); dep != nil {
			rsList, _ := client.ReplicaSets(q.Namespace, metav1.ListOptions{})
			groups = append(groups, deploymentRevisionEvents(dep, rsList))
			groups = append(groups, deploymentSpecEvents(dep))
		}
	}

	// StatefulSet: revision history lives in owned ControllerRevisions.
	if sets, err := client.StatefulSets(q.Namespace, metav1.ListOptions{}); err == nil {
		if set := findStatefulSet(sets, q.Workload); set != nil {
			crList, _ := client.ControllerRevisions(q.Namespace, metav1.ListOptions{})
			groups = append(groups, controllerRevisionEvents(q.Workload, crList))
			groups = append(groups, statefulSetSpecEvents(set))
		}
	}

	// DaemonSet: revision history also lives in owned ControllerRevisions.
	if sets, err := client.DaemonSets(q.Namespace, metav1.ListOptions{}); err == nil {
		if set := findDaemonSet(sets, q.Workload); set != nil {
			crList, _ := client.ControllerRevisions(q.Namespace, metav1.ListOptions{})
			groups = append(groups, controllerRevisionEvents(q.Workload, crList))
			groups = append(groups, daemonSetSpecEvents(set))
		}
	}

	// Autoscaler: any HPA targeting this workload contributes a scale baseline.
	if hpas, err := client.HPAs(q.Namespace, metav1.ListOptions{}); err == nil {
		groups = append(groups, hpaEvents(q.Workload, hpas))
	}

	// Events involving the workload object (mirrors the k8s.events selector).
	opts := metav1.ListOptions{FieldSelector: fmt.Sprintf("involvedObject.name=%s", q.Workload)}
	if evs, err := client.Events(q.Namespace, opts); err == nil {
		groups = append(groups, eventEvents(q.Workload, evs))
	}

	merged := MergeSorted(0, groups...)
	if !cutoff.IsZero() {
		merged = filterSince(merged, cutoff)
	}
	if q.Limit > 0 && len(merged) > q.Limit {
		merged = merged[:q.Limit]
	}
	return merged, nil
}

// findDeployment returns the Deployment named name in list, or nil.
func findDeployment(list *appsv1.DeploymentList, name string) *appsv1.Deployment {
	if list == nil {
		return nil
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i]
		}
	}
	return nil
}

// findStatefulSet returns the StatefulSet named name in list, or nil.
func findStatefulSet(list *appsv1.StatefulSetList, name string) *appsv1.StatefulSet {
	if list == nil {
		return nil
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i]
		}
	}
	return nil
}

// findDaemonSet returns the DaemonSet named name in list, or nil.
func findDaemonSet(list *appsv1.DaemonSetList, name string) *appsv1.DaemonSet {
	if list == nil {
		return nil
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i]
		}
	}
	return nil
}

// deploymentRevisionEvents reconstructs rollout history from the ReplicaSets
// owned by dep. Each owned ReplicaSet is one revision; its creation time is the
// rollout time and its pod-template image is the rolled-out image. Revisions
// are ordered newest-first, and each emits an "image" event whose Before is the
// image of the immediately older revision (empty for the oldest).
func deploymentRevisionEvents(dep *appsv1.Deployment, rsList *appsv1.ReplicaSetList) []ChangeEvent {
	if dep == nil || rsList == nil {
		return nil
	}
	type rev struct {
		num     int
		created time.Time
		image   string
	}
	var revs []rev
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !ownedBy(rs.OwnerReferences, "Deployment", dep.Name) {
			continue
		}
		revs = append(revs, rev{
			num:     parseRevision(rs.Annotations[deploymentRevisionAnnotation]),
			created: rs.CreationTimestamp.Time,
			image:   firstContainerImage(rs.Spec.Template.Spec.Containers),
		})
	}
	if len(revs) == 0 {
		return nil
	}
	// Oldest-first so Before can reference the prior revision's image.
	sort.SliceStable(revs, func(i, j int) bool { return revs[i].num < revs[j].num })

	out := make([]ChangeEvent, 0, len(revs))
	prevImage := ""
	for _, r := range revs {
		out = append(out, ChangeEvent{
			Time:    r.created,
			Kind:    "image",
			Target:  dep.Name,
			Summary: fmt.Sprintf("Deployment %s revision %d", dep.Name, r.num),
			Before:  prevImage,
			After:   r.image,
			Source:  "k8s",
		})
		prevImage = r.image
	}
	return out
}

// controllerRevisionEvents reconstructs rollout history from the
// ControllerRevisions owned by the StatefulSet/DaemonSet named workload. Each
// revision emits a "rollout" event at its creation time. ControllerRevision
// pod-template data is opaque (runtime.RawExtension), so no image diff is
// derivable here — Before/After are left empty.
func controllerRevisionEvents(workload string, crList *appsv1.ControllerRevisionList) []ChangeEvent {
	if crList == nil {
		return nil
	}
	var out []ChangeEvent
	for i := range crList.Items {
		cr := &crList.Items[i]
		if !ownedByName(cr.OwnerReferences, workload) {
			continue
		}
		out = append(out, ChangeEvent{
			Time:    cr.CreationTimestamp.Time,
			Kind:    "rollout",
			Target:  workload,
			Summary: fmt.Sprintf("%s revision %d", workload, cr.Revision),
			Source:  "k8s",
		})
	}
	return out
}

// deploymentSpecEvents emits a current-state baseline for a Deployment: its
// live image ("image") and its desired replica count ("scale"), both stamped
// at the workload's creation time.
func deploymentSpecEvents(dep *appsv1.Deployment) []ChangeEvent {
	if dep == nil {
		return nil
	}
	return specBaselineEvents(
		dep.Name,
		dep.CreationTimestamp.Time,
		firstContainerImage(dep.Spec.Template.Spec.Containers),
		replicaCount(dep.Spec.Replicas),
	)
}

// statefulSetSpecEvents emits the current-state baseline for a StatefulSet.
func statefulSetSpecEvents(set *appsv1.StatefulSet) []ChangeEvent {
	if set == nil {
		return nil
	}
	return specBaselineEvents(
		set.Name,
		set.CreationTimestamp.Time,
		firstContainerImage(set.Spec.Template.Spec.Containers),
		replicaCount(set.Spec.Replicas),
	)
}

// daemonSetSpecEvents emits the current-state baseline for a DaemonSet. A
// DaemonSet has no .Spec.Replicas (one pod per node), so only the image
// baseline is emitted.
func daemonSetSpecEvents(set *appsv1.DaemonSet) []ChangeEvent {
	if set == nil {
		return nil
	}
	img := firstContainerImage(set.Spec.Template.Spec.Containers)
	if img == "" {
		return nil
	}
	return []ChangeEvent{{
		Time:    set.CreationTimestamp.Time,
		Kind:    "image",
		Target:  set.Name,
		Summary: fmt.Sprintf("DaemonSet %s current image", set.Name),
		After:   img,
		Source:  "k8s",
	}}
}

// specBaselineEvents builds the image + scale baseline events shared by the
// Deployment and StatefulSet spec readers.
func specBaselineEvents(name string, created time.Time, image string, replicas int32) []ChangeEvent {
	var out []ChangeEvent
	if image != "" {
		out = append(out, ChangeEvent{
			Time:    created,
			Kind:    "image",
			Target:  name,
			Summary: fmt.Sprintf("%s current image", name),
			After:   image,
			Source:  "k8s",
		})
	}
	out = append(out, ChangeEvent{
		Time:    created,
		Kind:    "scale",
		Target:  name,
		Summary: fmt.Sprintf("%s desired replicas", name),
		After:   fmt.Sprintf("%d", replicas),
		Source:  "k8s",
	})
	return out
}

// hpaEvents emits a "scale" event for each HPA whose scaleTargetRef names the
// workload, carrying min/max/current replica bounds.
func hpaEvents(workload string, hpas *autoscalingv2.HorizontalPodAutoscalerList) []ChangeEvent {
	if hpas == nil {
		return nil
	}
	var out []ChangeEvent
	for i := range hpas.Items {
		hpa := &hpas.Items[i]
		if hpa.Spec.ScaleTargetRef.Name != workload {
			continue
		}
		min := int32(1)
		if hpa.Spec.MinReplicas != nil {
			min = *hpa.Spec.MinReplicas
		}
		out = append(out, ChangeEvent{
			Time:    hpa.CreationTimestamp.Time,
			Kind:    "scale",
			Target:  workload,
			Summary: fmt.Sprintf("HPA %s min=%d max=%d current=%d", hpa.Name, min, hpa.Spec.MaxReplicas, hpa.Status.CurrentReplicas),
			After:   fmt.Sprintf("%d", hpa.Status.CurrentReplicas),
			Source:  "k8s",
		})
	}
	return out
}

// eventEvents converts cluster Events whose involved object is workload into
// "event" ChangeEvents (reason + message), stamped at the event's last-seen
// time. The field-selector already narrows by name; we re-check defensively.
func eventEvents(workload string, evs *corev1.EventList) []ChangeEvent {
	if evs == nil {
		return nil
	}
	var out []ChangeEvent
	for i := range evs.Items {
		e := &evs.Items[i]
		if e.InvolvedObject.Name != "" && e.InvolvedObject.Name != workload {
			continue
		}
		out = append(out, ChangeEvent{
			Time:    eventTime(e),
			Kind:    "event",
			Target:  workload,
			Summary: fmt.Sprintf("%s: %s", e.Reason, e.Message),
			Source:  "k8s",
		})
	}
	return out
}

// filterSince drops events strictly older than cutoff.
func filterSince(events []ChangeEvent, cutoff time.Time) []ChangeEvent {
	out := events[:0:0]
	for _, e := range events {
		if e.Time.Before(cutoff) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ownedBy reports whether refs contains an owner of the given kind and name.
func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

// ownedByName reports whether refs contains any owner with the given name
// (kind-agnostic — used for ControllerRevisions which may be owned by either a
// StatefulSet or a DaemonSet).
func ownedByName(refs []metav1.OwnerReference, name string) bool {
	for _, r := range refs {
		if r.Name == name {
			return true
		}
	}
	return false
}

// firstContainerImage returns the image of the first container, or "".
func firstContainerImage(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	return containers[0].Image
}

// replicaCount dereferences a *int32 replica count, treating nil as 1 (the
// Kubernetes default when .Spec.Replicas is unset).
func replicaCount(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

// parseRevision parses the deployment revision annotation; non-numeric or empty
// values sort as 0.
func parseRevision(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// eventTime returns the most meaningful timestamp for an Event, preferring
// LastTimestamp, then EventTime, then the object creation time.
func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}
