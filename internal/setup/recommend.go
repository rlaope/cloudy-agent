package setup

import (
	"fmt"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/skills"
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
	added := make(map[string]bool, len(all))

	add := func(out []Recommendation, name, reason string) []Recommendation {
		if !known[name] || added[name] {
			return out
		}
		added[name] = true
		return append(out, Recommendation{SkillName: name, Reason: reason})
	}

	var out []Recommendation

	// Always-on skills.
	out = add(out, "service-health", "broad service health and user-impact triage")
	out = add(out, "k8s-incident", "general Kubernetes incident investigation")
	out = add(out, "prom-explorer", "Prometheus metrics exploration")
	out = add(out, "cluster-recon", "re-run cluster topology scans")

	if hasReachableK8sContext(p) {
		out = add(out, "triage-orchestrator", "reachable Kubernetes context detected — first-responder routing to the right deep skill")
	}
	if hasPrometheusReachableK8sContext(p) {
		out = add(out, "app-runtime-health", "Prometheus and reachable Kubernetes context detected — language-neutral application, framework, runtime, and service-layer p95/p99 triage available")
	}
	if hasObservableFrontendSurface(p) {
		out = add(out, "frontend-web-health", "frontend/web surface plus Prometheus or OpenTelemetry telemetry detected — Web Vitals, browser error, asset, CDN, and SSR/API triage available")
	}

	// GPU nodes present in any context.
	for _, cp := range p.Contexts {
		if cp.GPUNodeCount > 0 {
			out = add(out, "gpu-saturation",
				fmt.Sprintf("GPU nodes detected on %s", cp.Name))
			break
		}
	}

	for _, rule := range runtimeRecommendationRules {
		total := totalRuntimePodCount(p, rule)
		if total < runtimeRecommendationThreshold {
			continue
		}
		out = add(out, rule.skillName, fmt.Sprintf("%s workloads detected (%d pods) — %s", rule.label, total, rule.reason))
	}

	return out
}

const runtimeRecommendationThreshold = 3

var runtimeRecommendationRules = []runtimeRecommendationRule{
	{runtime: "go", label: "Go", skillName: "go-runtime", reason: "goroutine, GC pacing, scheduler, and pprof analysis available", requiresPrometheus: true},
	{runtime: "node", label: "Node.js / V8", skillName: "node-runtime", reason: "event-loop, GC, deopt, and V8 profile analysis available", requiresPrometheus: true},
	{runtime: "jvm", label: "JVM", skillName: "jvm-gc", reason: "GC analysis available", requiresPrometheus: true},
	{runtime: "jvm", label: "JVM", skillName: "jvm-thread", reason: "thread-dump and pool analysis available", requiresPrometheus: true},
	{runtime: "python", label: "Python", skillName: "py-perf", reason: "GIL, async-loop, and py-spy profiling analysis available", requiresPrometheus: true},
	{runtime: "ruby", label: "Ruby", skillName: "ruby-runtime", reason: "GVL, GC, YJIT, and rbspy stack analysis available", requiresPrometheus: true},
	{runtime: "dotnet", label: ".NET / CLR", skillName: "dotnet-runtime", reason: "GC, ThreadPool, and tiered-JIT metric analysis available", requiresPrometheus: true},
	{runtime: "native", label: "Native", skillName: "native-perf", reason: "perf hot-path, cache, branch, and lock-contention analysis available"},
}

type runtimeRecommendationRule struct {
	runtime            string
	label              string
	skillName          string
	reason             string
	requiresPrometheus bool
}

func totalRuntimePodCount(p config.Profile, rule runtimeRecommendationRule) int {
	var total int
	for _, cp := range p.Contexts {
		if !contextSupportsRuntimeRule(cp, rule) {
			continue
		}
		total += contextRuntimePodCount(cp, rule.runtime)
	}
	return total
}

func contextSupportsRuntimeRule(cp config.ContextProfile, rule runtimeRecommendationRule) bool {
	if !cp.Reachable {
		return false
	}
	return !rule.requiresPrometheus || cp.HasPrometheus
}

func contextRuntimePodCount(cp config.ContextProfile, runtime string) int {
	if cp.RuntimePodCounts != nil {
		if count, ok := cp.RuntimePodCounts[runtime]; ok {
			return count
		}
	}
	switch runtime {
	case "jvm":
		return cp.JVMPodCount
	case "python":
		return cp.PythonPodCount
	default:
		return 0
	}
}

func hasObservableFrontendSurface(p config.Profile) bool {
	for _, cp := range p.Contexts {
		if cp.Reachable && cp.HasFrontendSurface && (cp.HasPrometheus || cp.HasOTel) {
			return true
		}
	}
	return false
}

func hasReachableK8sContext(p config.Profile) bool {
	for _, cp := range p.Contexts {
		if cp.Reachable {
			return true
		}
	}
	return false
}

func hasPrometheusReachableK8sContext(p config.Profile) bool {
	for _, cp := range p.Contexts {
		if cp.HasPrometheus && cp.Reachable {
			return true
		}
	}
	return false
}
