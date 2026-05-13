package setup

import (
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/skills"
)

// allTestSkills returns a fake registry containing every skill name that
// Recommend may emit, so none are silently dropped.
func allTestSkills() []*skills.Skill {
	names := []string{
		"k8s-incident",
		"prom-explorer",
		"cluster-recon",
		"gpu-saturation",
		"jvm-gc",
		"jvm-thread",
		"py-perf",
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
		Contexts:      []config.ContextProfile{{Name: "ctx"}},
	}
	recs := Recommend(p, allTestSkills())

	for _, name := range []string{"k8s-incident", "prom-explorer", "cluster-recon"} {
		if !hasRec(recs, name) {
			t.Errorf("expected always-on skill %q in recommendations", name)
		}
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
