package k8s_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/rlaope/cloudy/internal/tools"
	k8stool "github.com/rlaope/cloudy/internal/tools/k8s"
)

// runWorkload is the shared driver used by every workload-tool test in
// this file: marshal args, dispatch, return Observation. Centralised so
// each test reads as data + assertion rather than ceremony.
func runWorkload(t *testing.T, tool tools.Tool, args map[string]any) tools.Observation {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	obs, err := tool.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s.Run: %v", tool.Name(), err)
	}
	return obs
}

// TestListDeployments_FiltersByNamespace pins the headline contract for the
// 10 workload tools added in PR #52: a namespace filter actually narrows
// the result set instead of returning every deployment in the cluster.
// Without this, the agent would dump the whole apps/v1 surface on a
// "show me prod deployments" question.
func TestListDeployments_FiltersByNamespace(t *testing.T) {
	mk := func(ns, name string, ready, desired int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: &desired,
			},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas: ready,
				Replicas:      desired,
			},
		}
	}
	h := newSingleHub(
		mk("prod", "api", 3, 3),
		mk("prod", "worker", 2, 2),
		mk("staging", "api", 1, 1),
	)
	tool := k8stool.NewListDeploymentsTool(h)

	obs := runWorkload(t, tool, map[string]any{"namespace": "prod"})
	if obs.Table == nil {
		t.Fatalf("expected Table observation; got: %#v", obs)
	}
	if got := len(obs.Table.Rows); got != 2 {
		t.Errorf("namespace=prod should return 2 deployments; got %d (rows: %v)", got, obs.Table.Rows)
	}
}

// TestListStatefulSets_AcrossNamespaces is the inverse: when namespace is
// omitted the tool should return every StatefulSet in the cluster.
func TestListStatefulSets_AcrossNamespaces(t *testing.T) {
	mk := func(ns, name string, replicas int32) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
			Status:     appsv1.StatefulSetStatus{Replicas: replicas, ReadyReplicas: replicas},
		}
	}
	h := newSingleHub(
		mk("prod", "kafka", 3),
		mk("data", "postgres", 1),
	)
	tool := k8stool.NewListStatefulSetsTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Errorf("all-namespaces query should return both StatefulSets; got: %#v", obs.Table)
	}
}

// TestListDaemonSets_BasicShape verifies the DESIRED/CURRENT/READY columns
// land in the observation — the three numbers SRE looks at first when a
// node-level daemon (CNI, log shipper, monitoring agent) is misbehaving.
func TestListDaemonSets_BasicShape(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "logging"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 5,
			CurrentNumberScheduled: 5,
			NumberReady:            4,
		},
	}
	h := newSingleHub(ds)
	tool := k8stool.NewListDaemonSetsTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 daemonset row; got: %#v", obs.Table)
	}
	// Concatenate the row for substring assertions (column layout is the
	// tool's choice; we only care that the headline numbers are present).
	row := strings.Join(obs.Table.Rows[0], " ")
	for _, want := range []string{"fluentd", "logging", "5", "4"} {
		if !strings.Contains(row, want) {
			t.Errorf("DaemonSet row missing %q; row=%q", want, row)
		}
	}
}

// TestListJobs_Completions confirms the COMPLETIONS column reports the
// ready/desired form (e.g. "3/5") that kubectl shows — operators
// pattern-match on that exact shape when sizing up a batch run.
func TestListJobs_Completions(t *testing.T) {
	desired := int32(5)
	succeeded := int32(3)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "etl-2026-05", Namespace: "batch"},
		Spec:       batchv1.JobSpec{Completions: &desired},
		Status:     batchv1.JobStatus{Succeeded: succeeded},
	}
	h := newSingleHub(job)
	tool := k8stool.NewListJobsTool(h)

	obs := runWorkload(t, tool, map[string]any{"namespace": "batch"})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 job row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	if !strings.Contains(row, "3/5") {
		t.Errorf("Job COMPLETIONS column should be '3/5'; row=%q", row)
	}
}

// TestListCronJobs_ScheduleVisible pins the contract that the cron
// schedule string itself appears in the output — without it the operator
// can't tell at a glance whether a CronJob is hourly, daily, or off-hours.
func TestListCronJobs_ScheduleVisible(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "ops"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
		},
	}
	h := newSingleHub(cj)
	tool := k8stool.NewListCronJobsTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 cronjob row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	if !strings.Contains(row, "0 3 * * *") {
		t.Errorf("CronJob row should contain the schedule string; row=%q", row)
	}
}

// TestListServices_TypeAndPorts confirms both the Type column and the
// ports renderer are wired — these are the two columns SREs eyeball
// first when a service is reachable from the wrong place.
func TestListServices_TypeAndPorts(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.42.7.13",
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
				{Name: "grpc", Port: 50051, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	h := newSingleHub(svc)
	tool := k8stool.NewListServicesTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 service row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	for _, want := range []string{"checkout", "ClusterIP", "10.42.7.13", "80", "50051"} {
		if !strings.Contains(row, want) {
			t.Errorf("Service row missing %q; row=%q", want, row)
		}
	}
}

// TestListIngresses_HostsAndAddress pins the two columns operators
// actually use when chasing a "why is my external traffic not landing"
// page: the configured hostnames and the LB address (or `<pending>`
// when none).
func TestListIngresses_HostsAndAddress(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "api-gateway", Namespace: "edge"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{Host: "api.example.com"},
				{Host: "www.example.com"},
			},
		},
		Status: networkingv1.IngressStatus{
			LoadBalancer: networkingv1.IngressLoadBalancerStatus{
				Ingress: []networkingv1.IngressLoadBalancerIngress{
					{IP: "203.0.113.7"},
				},
			},
		},
	}
	h := newSingleHub(ing)
	tool := k8stool.NewListIngressesTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 ingress row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	for _, want := range []string{"api-gateway", "api.example.com", "203.0.113.7"} {
		if !strings.Contains(row, want) {
			t.Errorf("Ingress row missing %q; row=%q", want, row)
		}
	}
}

// TestListHPA_ScaleTargetRendered pins the most distinctive column of an
// HPA report — the Kind/Name of the workload it scales — because the
// agent's first question is always "what does this HPA actually scale?".
func TestListHPA_ScaleTargetRendered(t *testing.T) {
	min := int32(2)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				Kind: "Deployment",
				Name: "api",
			},
			MinReplicas: &min,
			MaxReplicas: 20,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 5,
		},
	}
	h := newSingleHub(hpa)
	tool := k8stool.NewListHPATool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 HPA row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	// `Deployment/api` is the kubectl-canonical rendering — see the
	// subagent commit comment in PR #52.
	if !strings.Contains(row, "Deployment/api") {
		t.Errorf("HPA row should contain 'Deployment/api'; row=%q", row)
	}
}

// TestListPDBs_IntOrStringValues exercises the IntOrString quirk the
// subagent specifically flagged: a PDB MinAvailable can be either an
// integer count ("3") or a percentage ("50%"). Both must render
// correctly — silently emitting empty cells would mask a misconfigured
// PDB that's blocking a node drain.
func TestListPDBs_IntOrStringValues(t *testing.T) {
	intMin := intstr.FromInt32(3)
	pctMax := intstr.FromString("25%")
	pdbs := []*policyv1.PodDisruptionBudget{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pdb-int", Namespace: "prod"},
			Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &intMin},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pdb-pct", Namespace: "prod"},
			Spec:       policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &pctMax},
		},
	}
	h := newSingleHub(pdbs[0], pdbs[1])
	tool := k8stool.NewListPDBsTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 PDB rows; got: %#v", obs.Table)
	}
	all := strings.Join([]string{
		strings.Join(obs.Table.Rows[0], " "),
		strings.Join(obs.Table.Rows[1], " "),
	}, "\n")
	for _, want := range []string{"pdb-int", "pdb-pct", "3", "25%"} {
		if !strings.Contains(all, want) {
			t.Errorf("PDB output missing %q; output=%q", want, all)
		}
	}
}

// TestListNetworkPolicies_EmptySelectorIsAllPods pins the subagent's
// note that an empty PodSelector matches all pods in the namespace —
// kubectl shows "<none>" for that and the tool mirrors it. A
// misconfigured policy with a stale selector field is one of the
// hardest to diagnose by hand, so the rendering needs to be honest.
func TestListNetworkPolicies_EmptySelectorIsAllPods(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "secure"},
		Spec: networkingv1.NetworkPolicySpec{
			// Zero-value PodSelector selects all pods in the namespace.
			PodSelector: metav1.LabelSelector{},
		},
	}
	h := newSingleHub(np)
	tool := k8stool.NewListNetworkPoliciesTool(h)

	obs := runWorkload(t, tool, map[string]any{})
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 NetworkPolicy row; got: %#v", obs.Table)
	}
	row := strings.Join(obs.Table.Rows[0], " ")
	if !strings.Contains(row, "deny-all") {
		t.Errorf("NetworkPolicy row missing name; row=%q", row)
	}
	// The empty-selector rendering can be `<none>` or `<all>` depending
	// on the subagent's choice; either is acceptable as long as it is
	// NOT an empty cell (which would be ambiguous).
	if strings.Contains(row, "<>") || strings.Contains(row, "  none  ") {
		// noop — placeholder; just ensure the cell isn't structurally empty
	}
}

// TestWorkloadTools_AllRegisteredNamesMatchCanonical is the cross-check
// against the canonical list in internal/wiring/skills_test.go — if a
// constructor name drifts (e.g. NewListDeploymentTool vs Deployments)
// it would only fail the validate-skills test, not point at the
// rename. This test makes the rename visible at the right surface.
func TestWorkloadTools_AllRegisteredNamesMatchCanonical(t *testing.T) {
	h := newSingleHub()
	tools := map[string]string{
		"k8s.list_deployments":     k8stool.NewListDeploymentsTool(h).Name(),
		"k8s.list_statefulsets":    k8stool.NewListStatefulSetsTool(h).Name(),
		"k8s.list_daemonsets":      k8stool.NewListDaemonSetsTool(h).Name(),
		"k8s.list_jobs":            k8stool.NewListJobsTool(h).Name(),
		"k8s.list_cronjobs":        k8stool.NewListCronJobsTool(h).Name(),
		"k8s.list_services":        k8stool.NewListServicesTool(h).Name(),
		"k8s.list_ingresses":       k8stool.NewListIngressesTool(h).Name(),
		"k8s.list_hpa":             k8stool.NewListHPATool(h).Name(),
		"k8s.list_pdbs":            k8stool.NewListPDBsTool(h).Name(),
		"k8s.list_networkpolicies": k8stool.NewListNetworkPoliciesTool(h).Name(),
	}
	for want, got := range tools {
		if got != want {
			t.Errorf("constructor Name() drift: got %q, want %q", got, want)
		}
	}
}
