package setup

import (
	"fmt"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/skills"
)

// Recommendation pairs a skill name with a human-readable reason for the
// suggestion.
type Recommendation struct {
	SkillName string
	Reason    string
}

// Recommend computes the list of skills that should be enabled given the
// discovered cluster profile. all is the full registry of available skills;
// only skills that exist in the registry are recommended (unknown names are
// silently dropped).
func Recommend(p config.Profile, all []*skills.Skill) []Recommendation {
	known := make(map[string]bool, len(all))
	for _, s := range all {
		known[s.Name] = true
	}

	add := func(out []Recommendation, name, reason string) []Recommendation {
		if !known[name] {
			return out
		}
		return append(out, Recommendation{SkillName: name, Reason: reason})
	}

	var out []Recommendation

	// Always-on skills.
	out = add(out, "k8s-incident", "general Kubernetes incident investigation")
	out = add(out, "prom-explorer", "Prometheus metrics exploration")
	out = add(out, "cluster-recon", "re-run cluster topology scans")

	// GPU nodes present in any context.
	for _, cp := range p.Contexts {
		if cp.GPUNodeCount > 0 {
			out = add(out, "gpu-saturation",
				fmt.Sprintf("GPU nodes detected on %s", cp.Name))
			break
		}
	}

	// JVM workload threshold.
	var totalJVM int
	for _, cp := range p.Contexts {
		totalJVM += cp.JVMPodCount
	}
	if totalJVM >= 3 {
		out = add(out, "jvm-gc", "JVM workloads detected — GC analysis available")
		out = add(out, "jvm-thread", "JVM workloads detected — thread-dump analysis available")
	}

	// Python workload threshold.
	var totalPython int
	for _, cp := range p.Contexts {
		totalPython += cp.PythonPodCount
	}
	if totalPython >= 3 {
		out = add(out, "py-perf", "Python workloads detected — performance profiling available")
	}

	return out
}
