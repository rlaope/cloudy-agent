package setup

import (
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/skills"
)

// allTestSkills returns a fake registry containing every skill name that
// Recommend may emit, so none are silently dropped.
func allTestSkills() []*skills.Skill {
	names := []string{
		"service-health",
		"app-runtime-health",
		"frontend-web-health",
		"triage-orchestrator",
		"k8s-incident",
		"prom-explorer",
		"cluster-recon",
		"gpu-saturation",
		"go-runtime",
		"node-runtime",
		"jvm-gc",
		"jvm-thread",
		"py-perf",
		"ruby-runtime",
		"dotnet-runtime",
		"native-perf",
	}
	out := make([]*skills.Skill, 0, len(names))
	for _, n := range names {
		out = append(out, &skills.Skill{
			Name:         n,
			Description:  n,
			AllowedTools: []string{"k8s_list_pods"},
			SystemPrompt: "placeholder",
		})
	}
	return out
}

func hasRec(recs []Recommendation, name string) bool {
	for _, r := range recs {
		if r.SkillName == name {
			return true
		}
	}
	return false
}

// TestRecommend_AlwaysOn verifies always-on skills are present regardless of profile.
func TestRecommend_AlwaysOn(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: true}},
	}
	recs := Recommend(p, allTestSkills())

	for _, name := range []string{"service-health", "triage-orchestrator", "k8s-incident", "prom-explorer", "cluster-recon"} {
		if !hasRec(recs, name) {
			t.Errorf("expected always-on skill %q in recommendations", name)
		}
	}
}

func TestRecommend_TriageOrchestratorRequiresReachableK8s(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: false}},
	}
	recs := Recommend(p, allTestSkills())
	if hasRec(recs, "triage-orchestrator") {
		t.Error("did not expect triage-orchestrator without a reachable Kubernetes context")
	}
	if !hasRec(recs, "service-health") {
		t.Error("expected service-health to remain the broad default without Kubernetes")
	}
}

func TestRecommend_AppRuntimeHealthRequiresPrometheusAndReachableK8s(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: true, HasPrometheus: true}},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "app-runtime-health") {
		t.Error("expected app-runtime-health when Prometheus is discovered")
	}
}

func TestRecommend_AppRuntimeHealthAbsentWithoutReachableK8s(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: false, HasPrometheus: true}},
	}
	recs := Recommend(p, allTestSkills())
	if hasRec(recs, "app-runtime-health") {
		t.Error("did not expect app-runtime-health without a reachable Kubernetes context")
	}
}

func TestRecommend_AppRuntimeHealthAbsentWithoutPrometheus(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: true, HasPrometheus: false}},
	}
	recs := Recommend(p, allTestSkills())
	if hasRec(recs, "app-runtime-health") {
		t.Error("did not expect app-runtime-health without Prometheus")
	}
}

func TestRecommend_FrontendWebHealthRequiresObservableFrontendSurface(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{{
			Name:               "ctx",
			Reachable:          true,
			HasPrometheus:      true,
			HasFrontendSurface: true,
			FrontendPodCount:   2,
			IngressHostCount:   1,
		}},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "frontend-web-health") {
		t.Error("expected frontend-web-health when a reachable frontend surface has telemetry")
	}
}

func TestRecommend_FrontendWebHealthAllowsOTelTelemetry(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{{
			Name:               "ctx",
			Reachable:          true,
			HasOTel:            true,
			HasFrontendSurface: true,
			IngressHostCount:   1,
		}},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "frontend-web-health") {
		t.Error("expected frontend-web-health when a reachable frontend surface has OTel")
	}
}

func TestRecommend_FrontendWebHealthAbsentWithoutSurfaceOrTelemetry(t *testing.T) {
	cases := []struct {
		name string
		cp   config.ContextProfile
	}{
		{
			name: "telemetry without frontend surface",
			cp:   config.ContextProfile{Name: "ctx", Reachable: true, HasPrometheus: true},
		},
		{
			name: "frontend surface without telemetry",
			cp:   config.ContextProfile{Name: "ctx", Reachable: true, HasFrontendSurface: true, FrontendPodCount: 1},
		},
		{
			name: "unreachable frontend surface",
			cp:   config.ContextProfile{Name: "ctx", Reachable: false, HasPrometheus: true, HasFrontendSurface: true, IngressHostCount: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := config.Profile{
				SchemaVersion: config.CurrentSchemaVersion,
				Contexts:      []config.ContextProfile{tc.cp},
			}
			recs := Recommend(p, allTestSkills())
			if hasRec(recs, "frontend-web-health") {
				t.Errorf("did not expect frontend-web-health for %s", tc.name)
			}
		})
	}
}

// TestRecommend_GPU verifies gpu-saturation is recommended when GPU nodes exist.
func TestRecommend_GPU(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{
			{Name: "gpu-ctx", GPUNodeCount: 2},
		},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "gpu-saturation") {
		t.Error("expected gpu-saturation when GPUNodeCount > 0")
	}
}

// TestRecommend_NoGPU verifies gpu-saturation is absent when no GPU nodes exist.
func TestRecommend_NoGPU(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", GPUNodeCount: 0}},
	}
	recs := Recommend(p, allTestSkills())
	if hasRec(recs, "gpu-saturation") {
		t.Error("did not expect gpu-saturation without GPU nodes")
	}
}

// TestRecommend_JVM verifies jvm-gc and jvm-thread when total JVM pods >= 3.
func TestRecommend_JVM(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{
			{Name: "ctx", JVMPodCount: 5},
		},
	}
	recs := Recommend(p, allTestSkills())
	for _, name := range []string{"jvm-gc", "jvm-thread"} {
		if !hasRec(recs, name) {
			t.Errorf("expected %q with 5 JVM pods", name)
		}
	}
}

// TestRecommend_JVMBelowThreshold verifies jvm skills absent when < 3 JVM pods.
func TestRecommend_JVMBelowThreshold(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", JVMPodCount: 2}},
	}
	recs := Recommend(p, allTestSkills())
	for _, name := range []string{"jvm-gc", "jvm-thread"} {
		if hasRec(recs, name) {
			t.Errorf("did not expect %q with only 2 JVM pods", name)
		}
	}
}

// TestRecommend_Python verifies py-perf when total Python pods >= 3.
func TestRecommend_Python(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", PythonPodCount: 4}},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "py-perf") {
		t.Error("expected py-perf with 4 Python pods")
	}
}

// TestRecommend_PythonBelowThreshold verifies py-perf absent when < 3 Python pods.
func TestRecommend_PythonBelowThreshold(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", PythonPodCount: 1}},
	}
	recs := Recommend(p, allTestSkills())
	if hasRec(recs, "py-perf") {
		t.Error("did not expect py-perf with only 1 Python pod")
	}
}

func TestRecommend_GenericRuntimeFamilies(t *testing.T) {
	cases := []struct {
		runtime string
		skills  []string
	}{
		{runtime: "go", skills: []string{"go-runtime"}},
		{runtime: "node", skills: []string{"node-runtime"}},
		{runtime: "jvm", skills: []string{"jvm-gc", "jvm-thread"}},
		{runtime: "python", skills: []string{"py-perf"}},
		{runtime: "ruby", skills: []string{"ruby-runtime"}},
		{runtime: "dotnet", skills: []string{"dotnet-runtime"}},
		{runtime: "native", skills: []string{"native-perf"}},
	}

	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			p := config.Profile{
				SchemaVersion: config.CurrentSchemaVersion,
				Contexts: []config.ContextProfile{{
					Name:             "ctx",
					RuntimePodCounts: map[string]int{tc.runtime: 3},
				}},
			}
			recs := Recommend(p, allTestSkills())
			for _, skill := range tc.skills {
				if !hasRec(recs, skill) {
					t.Errorf("expected %q for runtime %q", skill, tc.runtime)
				}
			}
		})
	}
}

func TestRecommend_GenericRuntimeFamiliesBelowThreshold(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{{
			Name: "ctx",
			RuntimePodCounts: map[string]int{
				"go":     2,
				"node":   2,
				"ruby":   2,
				"dotnet": 2,
				"native": 2,
			},
		}},
	}
	recs := Recommend(p, allTestSkills())
	for _, name := range []string{"go-runtime", "node-runtime", "ruby-runtime", "dotnet-runtime", "native-perf"} {
		if hasRec(recs, name) {
			t.Errorf("did not expect %q below runtime pod threshold", name)
		}
	}
}

// TestRecommend_UnknownSkillDropped verifies that skills not in the registry
// are silently dropped.
func TestRecommend_UnknownSkillDropped(t *testing.T) {
	// Empty registry — nothing should be returned even for GPU nodes.
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx", GPUNodeCount: 4}},
	}
	recs := Recommend(p, nil)
	if len(recs) != 0 {
		t.Errorf("expected empty recommendations with empty registry, got %v", recs)
	}
}

// TestRecommend_MultiContextJVM verifies JVM count is summed across contexts.
func TestRecommend_MultiContextJVM(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{
			{Name: "a", JVMPodCount: 1},
			{Name: "b", JVMPodCount: 2},
		},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "jvm-gc") {
		t.Error("expected jvm-gc when sum of JVMPodCount across contexts >= 3")
	}
}

func TestRecommend_MultiContextRuntimePodCounts(t *testing.T) {
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts: []config.ContextProfile{
			{Name: "a", RuntimePodCounts: map[string]int{"node": 1}},
			{Name: "b", RuntimePodCounts: map[string]int{"node": 2}},
		},
	}
	recs := Recommend(p, allTestSkills())
	if !hasRec(recs, "node-runtime") {
		t.Error("expected node-runtime when runtime_pod_counts sum across contexts >= 3")
	}
}
