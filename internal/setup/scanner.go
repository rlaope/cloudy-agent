// Package setup implements the first-run setup wizard, gate, scanner,
// recommender, and doctor for cloudy.
package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/transport"
)

// ContextResult is a type alias for config.ContextProfile, used by the scanner
// to report the result of probing a single kubeconfig context.
type ContextResult = config.ContextProfile

// jvmEnvVars is the set of environment variable names that signal a JVM process.
var jvmEnvVars = map[string]bool{
	"JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS":     true,
	"JAVA_OPTS":         true,
}

// pythonEnvVars is the set of environment variable names that signal a Python process.
var pythonEnvVars = map[string]bool{
	"PYTHONPATH":       true,
	"PYTHONUNBUFFERED": true,
}

// jvmImageRe matches image names that suggest a JVM runtime.
var jvmImageRe = regexp.MustCompile(`jdk|jre|openjdk|:\d+-jdk|:\d+-jre`)

// pythonImageRe matches image names that suggest a Python runtime.
var pythonImageRe = regexp.MustCompile(`python`)

// ScanContext probes a single Kubernetes context and returns a ContextResult
// with the discovered topology. If the API server is unreachable the result has
// Reachable=false and no error is returned — the caller decides what to do.
//
// All operations use only list/get verbs.
func ScanContext(ctx context.Context, kubeconfigPath, contextName string) (ContextResult, error) {
	result := ContextResult{Name: contextName}

	core, err := buildCoreClient(kubeconfigPath, contextName)
	if err != nil {
		return result, fmt.Errorf("setup: build client for %q: %w", contextName, err)
	}

	// Reachability: probe /version with a 5s timeout.
	vCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	version, reachable := probeVersion(vCtx, core)
	result.Reachable = reachable
	if !reachable {
		return result, nil
	}
	result.K8sVersion = version

	// Nodes.
	nodes, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		nodes = &corev1.NodeList{}
	}
	result.NodeCount = len(nodes.Items)
	for _, n := range nodes.Items {
		if isGPUNode(n) {
			result.GPUNodeCount++
		}
	}

	// Namespaces (up to 200).
	nsList, err := core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		nsList = &corev1.NamespaceList{}
	}
	nsNames := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		nsNames = append(nsNames, ns.Name)
	}
	result.Namespaces = nsNames

	// Sample pods (up to 500 across all namespaces).
	pods, err := core.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		pods = &corev1.PodList{}
	}
	for _, p := range pods.Items {
		if isJVMPod(p) {
			result.JVMPodCount++
		}
		if isPythonPod(p) {
			result.PythonPodCount++
		}
	}

	// Component detection.
	detectComponents(ctx, core, nsNames, &result)

	return result, nil
}

// buildCoreClient constructs a read-only kubernetes.Interface for the given
// kubeconfig path and context, using transport.Wrap to enforce the read-only
// contract at the HTTP layer.
func buildCoreClient(kubeconfigPath, contextName string) (kubernetes.Interface, error) {
	rules := &clientcmd.ClientConfigLoadingRules{}
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	} else {
		home, _ := os.UserHomeDir()
		rules.ExplicitPath = filepath.Join(home, ".kube", "config")
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.WrapTransport = transport.Wrap
	return kubernetes.NewForConfig(cfg)
}

// probeVersion calls the discovery API's server version endpoint. Returns
// (version, true) on success, ("", false) when the server is unreachable.
func probeVersion(ctx context.Context, core kubernetes.Interface) (string, bool) {
	// Use a goroutine so the ctx deadline is respected.
	type result struct {
		ver string
		ok  bool
	}
	ch := make(chan result, 1)
	go func() {
		sv, err := core.Discovery().ServerVersion()
		if err != nil {
			ch <- result{}
			return
		}
		ch <- result{ver: sv.GitVersion, ok: true}
	}()
	select {
	case <-ctx.Done():
		return "", false
	case r := <-ch:
		return r.ver, r.ok
	}
}

// isGPUNode returns true when the node exposes nvidia.com/gpu allocatable
// resources or has labels containing "gpu" or "nvidia".
func isGPUNode(n corev1.Node) bool {
	if q, ok := n.Status.Allocatable["nvidia.com/gpu"]; ok {
		zero := resource.MustParse("0")
		if q.Cmp(zero) > 0 {
			return true
		}
	}
	for k, v := range n.Labels {
		kl, vl := strings.ToLower(k), strings.ToLower(v)
		if strings.Contains(kl, "gpu") || strings.Contains(kl, "nvidia") ||
			strings.Contains(vl, "gpu") || strings.Contains(vl, "nvidia") {
			return true
		}
	}
	return false
}

// isJVMPod returns true when any container in the pod looks like a JVM process.
func isJVMPod(p corev1.Pod) bool {
	for _, c := range p.Spec.Containers {
		if jvmImageRe.MatchString(strings.ToLower(c.Image)) {
			return true
		}
		for _, env := range c.Env {
			if jvmEnvVars[env.Name] {
				return true
			}
		}
	}
	return false
}

// isPythonPod returns true when any container in the pod looks like a Python process.
func isPythonPod(p corev1.Pod) bool {
	for _, c := range p.Spec.Containers {
		if pythonImageRe.MatchString(strings.ToLower(c.Image)) {
			return true
		}
		for _, env := range c.Env {
			if pythonEnvVars[env.Name] {
				return true
			}
		}
	}
	return false
}

// detectComponents probes for well-known infrastructure components.
func detectComponents(ctx context.Context, core kubernetes.Interface, namespaces []string, result *ContextResult) {
	// metrics-server: Deployment named "metrics-server" in kube-system.
	deps, err := core.AppsV1().Deployments("kube-system").List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, d := range deps.Items {
			if d.Name == "metrics-server" {
				result.HasMetricsServer = true
				break
			}
		}
	}

	// Scan services in each namespace for Prometheus, DCGM, OTel.
	for _, ns := range namespaces {
		svcs, err := core.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, svc := range svcs.Items {
			name := strings.ToLower(svc.Name)
			appName := strings.ToLower(svc.Labels["app.kubernetes.io/name"])

			if strings.Contains(name, "prometheus") || appName == "prometheus" {
				result.HasPrometheus = true
				url := fmt.Sprintf("http://%s.%s.svc:9090", svc.Name, ns)
				result.PrometheusURLs = appendUnique(result.PrometheusURLs, url)
			}

			if strings.Contains(name, "dcgm") {
				result.HasDCGMExporter = true
			}

			if strings.Contains(name, "otel") || strings.Contains(appName, "opentelemetry") {
				result.HasOTel = true
			}
		}

		// DaemonSets for DCGM.
		if !result.HasDCGMExporter {
			ds, err := core.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
			if err == nil {
				for _, d := range ds.Items {
					if strings.Contains(strings.ToLower(d.Name), "dcgm") {
						result.HasDCGMExporter = true
						break
					}
				}
			}
		}
	}
}

// appendUnique appends s to slice only if it is not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
