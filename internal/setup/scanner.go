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
	"unicode"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/transport"
)

// canonicalPermissionProbes is the fixed set of (group, resource, subresource,
// verb) tuples cloudy actually exercises. Surfaced through ContextProfile so
// /setup tells users up front whether the active credential can do the work
// the agent will try, rather than failing mid-conversation.
var canonicalPermissionProbes = []struct {
	group       string
	resource    string
	subresource string
	verb        string
}{
	{"", "pods", "", "list"},
	{"", "pods", "", "get"},
	{"", "pods", "log", "get"},
	{"", "nodes", "", "list"},
	{"", "services", "", "list"},
	{"", "events", "", "list"},
	{"", "namespaces", "", "list"},
	{"networking.k8s.io", "ingresses", "", "list"},
	{"apps", "deployments", "", "list"},
	{"apps", "daemonsets", "", "list"},
	{"metrics.k8s.io", "pods", "", "list"},
	{"metrics.k8s.io", "nodes", "", "list"},
}

const (
	setupPodScanPageLimit int64 = 500
	setupPodScanMaxPods   int   = 2000

	setupPodSampleIncompleteCap       = "cap"
	setupPodSampleIncompleteListError = "list_error"
)

// ContextResult is a type alias for config.ContextProfile, used by the scanner
// to report the result of probing a single kubeconfig context.
type ContextResult = config.ContextProfile

type runtimeSignal struct {
	name           string
	imageRe        *regexp.Regexp
	blockedImageRe *regexp.Regexp
	envNames       map[string]bool
	envPrefixes    []string
	tokens         map[string]bool
}

var runtimeSignals = []runtimeSignal{
	{
		name:    "go",
		imageRe: regexp.MustCompile(`(^|[/:._-])(go|golang)($|[/:._-])`),
		envNames: map[string]bool{
			"GODEBUG":    true,
			"GOGC":       true,
			"GOMEMLIMIT": true,
			"GOMAXPROCS": true,
		},
		tokens: map[string]bool{
			"go":     true,
			"golang": true,
		},
	},
	{
		name:           "node",
		imageRe:        regexp.MustCompile(`(^|[/:._-])(node|nodejs|next|nextjs|nestjs|express)($|[/:._-])`),
		blockedImageRe: regexp.MustCompile(`(^|[/:._-])(node-exporter|prometheus-node-exporter)($|[/:._-])`),
		envNames: map[string]bool{
			"NODE_ENV":            true,
			"NODE_OPTIONS":        true,
			"NPM_CONFIG_LOGLEVEL": true,
		},
		tokens: map[string]bool{
			"node":    true,
			"nodejs":  true,
			"next":    true,
			"nextjs":  true,
			"nestjs":  true,
			"express": true,
		},
	},
	{
		name:    "jvm",
		imageRe: regexp.MustCompile(`(^|[/:._-])(java|jdk|jre|openjdk|eclipse-temurin|temurin|amazoncorretto|corretto)($|[/:._-])|:\d+-jdk|:\d+-jre`),
		envNames: map[string]bool{
			"JAVA_TOOL_OPTIONS": true,
			"_JAVA_OPTIONS":     true,
			"JAVA_OPTS":         true,
		},
		tokens: map[string]bool{
			"java":            true,
			"jdk":             true,
			"jre":             true,
			"jvm":             true,
			"openjdk":         true,
			"eclipse-temurin": true,
			"temurin":         true,
			"corretto":        true,
			"spring":          true,
			"tomcat":          true,
			"netty":           true,
		},
	},
	{
		name:    "python",
		imageRe: regexp.MustCompile(`(^|[/:._-])(python|django|fastapi|flask|gunicorn|uvicorn|celery)($|[/:._-])`),
		envNames: map[string]bool{
			"PYTHONPATH":       true,
			"PYTHONUNBUFFERED": true,
		},
		tokens: map[string]bool{
			"python":   true,
			"django":   true,
			"fastapi":  true,
			"flask":    true,
			"gunicorn": true,
			"uvicorn":  true,
			"celery":   true,
		},
	},
	{
		name:    "ruby",
		imageRe: regexp.MustCompile(`(^|[/:._-])(ruby|rails|puma|sidekiq)($|[/:._-])`),
		envNames: map[string]bool{
			"BUNDLE_GEMFILE": true,
			"RAILS_ENV":      true,
			"RACK_ENV":       true,
			"RUBYOPT":        true,
		},
		tokens: map[string]bool{
			"ruby":    true,
			"rails":   true,
			"puma":    true,
			"sidekiq": true,
		},
	},
	{
		name:    "dotnet",
		imageRe: regexp.MustCompile(`mcr\.microsoft\.com/dotnet|(^|[/:._-])(dotnet|aspnet|aspnetcore|clr)($|[/:._-])`),
		envPrefixes: []string{
			"ASPNETCORE_",
			"COMPlus_",
			"DOTNET_",
		},
		tokens: map[string]bool{
			"dotnet":     true,
			"aspnet":     true,
			"aspnetcore": true,
			"clr":        true,
		},
	},
	{
		name:    "native",
		imageRe: regexp.MustCompile(`(^|[/:._-])(rust|rustlang|cpp|cxx|clang|gcc|zig|native)($|[/:._-])`),
		tokens: map[string]bool{
			"rust":     true,
			"rustlang": true,
			"cpp":      true,
			"cxx":      true,
			"clang":    true,
			"gcc":      true,
			"zig":      true,
			"native":   true,
		},
	},
}

// frontendEnvPrefixes are public browser-build environment variable prefixes
// commonly present in web app containers.
var frontendEnvPrefixes = []string{"NEXT_PUBLIC_", "VITE_", "REACT_APP_"}

var frontendHintTokens = map[string]bool{
	"frontend": true,
	"web":      true,
	"webapp":   true,
	"www":      true,
	"static":   true,
	"spa":      true,
	"ui":       true,
	"nginx":    true,
	"httpd":    true,
	"caddy":    true,
	"next":     true,
	"nextjs":   true,
	"vite":     true,
	"react":    true,
}

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

	// SelfSubjectAccessReview against the canonical verb/resource pairs
	// cloudy uses, so /setup surfaces real RBAC capability per context.
	result.Permissions = probePermissions(ctx, core)

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

	// Sample pods across namespaces with pagination, capped to keep setup fast
	// on very large clusters while avoiding first-page-only bias.
	podSample := listPodsForSetup(ctx, core)
	result.PodSampleScanned = true
	result.PodSampleCount = len(podSample.Items)
	result.PodSampleIncomplete = podSample.Incomplete
	result.PodSampleIncompleteReason = podSample.IncompleteReason
	for _, p := range podSample.Items {
		runtimes := detectPodRuntimes(p)
		recordRuntimePodCounts(&result, runtimes)
		if containsRuntime(runtimes, "jvm") {
			result.JVMPodCount++
		}
		if containsRuntime(runtimes, "python") {
			result.PythonPodCount++
		}
		if isFrontendPod(p) {
			result.FrontendPodCount++
			result.HasFrontendSurface = true
		}
	}

	// Component detection.
	detectComponents(ctx, core, nsNames, &result)

	return result, nil
}

type podListFunc func(context.Context, metav1.ListOptions) (*corev1.PodList, error)

type podScanSample struct {
	Items            []corev1.Pod
	Incomplete       bool
	IncompleteReason string
}

func listPodsForSetup(ctx context.Context, core kubernetes.Interface) podScanSample {
	return listPodsForSetupWith(ctx, func(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		return core.CoreV1().Pods("").List(ctx, opts)
	})
}

func listPodsForSetupWith(ctx context.Context, list podListFunc) podScanSample {
	var out podScanSample
	opts := metav1.ListOptions{Limit: setupPodScanPageLimit}
	for len(out.Items) < setupPodScanMaxPods {
		page, err := list(ctx, opts)
		if err != nil || page == nil {
			out.Incomplete = true
			out.IncompleteReason = setupPodSampleIncompleteListError
			return out
		}
		remaining := setupPodScanMaxPods - len(out.Items)
		if len(page.Items) > remaining {
			out.Items = append(out.Items, page.Items[:remaining]...)
			out.Incomplete = true
			out.IncompleteReason = setupPodSampleIncompleteCap
			return out
		}
		out.Items = append(out.Items, page.Items...)
		if page.Continue == "" {
			return out
		}
		opts.Continue = page.Continue
	}
	out.Incomplete = true
	out.IncompleteReason = setupPodSampleIncompleteCap
	return out
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

// probePermissions runs a SelfSubjectAccessReview for every probe in
// canonicalPermissionProbes and returns the results. An individual SSAR
// failure surfaces as Allowed=false with the error message in Reason — that
// is the same way a real RBAC denial reads to a user, so the wizard can
// render both uniformly.
func probePermissions(ctx context.Context, core kubernetes.Interface) []config.PermissionCheck {
	out := make([]config.PermissionCheck, 0, len(canonicalPermissionProbes))
	for _, p := range canonicalPermissionProbes {
		ssar := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group:       p.group,
					Resource:    p.resource,
					Subresource: p.subresource,
					Verb:        p.verb,
				},
			},
		}
		check := config.PermissionCheck{
			Group:       p.group,
			Resource:    p.resource,
			Subresource: p.subresource,
			Verb:        p.verb,
		}
		resp, err := core.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
		if err != nil {
			check.Reason = err.Error()
			out = append(out, check)
			continue
		}
		check.Allowed = resp.Status.Allowed
		if !check.Allowed {
			check.Reason = resp.Status.Reason
		}
		out = append(out, check)
	}
	return out
}

func recordRuntimePodCounts(result *ContextResult, runtimes []string) {
	if len(runtimes) == 0 {
		return
	}
	if result.RuntimePodCounts == nil {
		result.RuntimePodCounts = make(map[string]int, len(runtimes))
	}
	for _, runtime := range runtimes {
		result.RuntimePodCounts[runtime]++
	}
}

// detectPodRuntimes returns best-effort runtime family hints for setup
// recommendations. It is intentionally broad: generic service, log, trace, and
// metric triage still works without a runtime match.
func detectPodRuntimes(p corev1.Pod) []string {
	var out []string
	for _, signal := range runtimeSignals {
		if podMatchesRuntimeSignal(p, signal) {
			out = append(out, signal.name)
		}
	}
	return out
}

func podMatchesRuntimeSignal(p corev1.Pod, signal runtimeSignal) bool {
	for k, v := range p.Labels {
		if hasRuntimeMetadataHint(signal, k, v) {
			return true
		}
	}
	for k, v := range p.Annotations {
		if hasRuntimeMetadataHint(signal, k, v) {
			return true
		}
	}
	for _, c := range p.Spec.Containers {
		if imageMatchesRuntimeSignal(signal, c.Image) {
			return true
		}
		for _, env := range c.Env {
			if runtimeMatchesEnv(signal, env.Name) {
				return true
			}
		}
	}
	return false
}

func imageMatchesRuntimeSignal(signal runtimeSignal, image string) bool {
	lower := strings.ToLower(image)
	if signal.blockedImageRe != nil && signal.blockedImageRe.MatchString(lower) {
		return false
	}
	return signal.imageRe != nil && signal.imageRe.MatchString(lower)
}

func hasRuntimeMetadataHint(signal runtimeSignal, key, value string) bool {
	if !metadataKeySuggestsRuntime(key) {
		return false
	}
	return hasRuntimeToken(signal, value)
}

func metadataKeySuggestsRuntime(key string) bool {
	for _, token := range tokenizeHints(key) {
		switch token {
		case "framework", "language", "platform", "runtime", "stack":
			return true
		}
	}
	return false
}

func runtimeMatchesEnv(signal runtimeSignal, name string) bool {
	upper := strings.ToUpper(name)
	if signal.envNames[upper] {
		return true
	}
	for _, prefix := range signal.envPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func containsRuntime(runtimes []string, name string) bool {
	for _, runtime := range runtimes {
		if runtime == name {
			return true
		}
	}
	return false
}

func hasRuntimeToken(signal runtimeSignal, values ...string) bool {
	for _, token := range tokenizeHints(values...) {
		if signal.tokens[token] {
			return true
		}
	}
	return false
}

// isJVMPod returns true when any container in the pod looks like a JVM process.
func isJVMPod(p corev1.Pod) bool {
	return containsRuntime(detectPodRuntimes(p), "jvm")
}

// isPythonPod returns true when any container in the pod looks like a Python process.
func isPythonPod(p corev1.Pod) bool {
	return containsRuntime(detectPodRuntimes(p), "python")
}

// isFrontendPod returns true when a pod looks like a browser-facing web app or
// static frontend surface. It intentionally avoids treating every Node.js image
// as frontend, since many Node services are server-only APIs.
func isFrontendPod(p corev1.Pod) bool {
	if hasFrontendHint(p.Name, p.GenerateName) {
		return true
	}
	for k, v := range p.Labels {
		if hasFrontendHint(k, v) {
			return true
		}
	}
	for k, v := range p.Annotations {
		if hasFrontendHint(k, v) {
			return true
		}
	}
	for _, c := range p.Spec.Containers {
		if hasFrontendHint(c.Name, c.Image) {
			return true
		}
		for _, env := range c.Env {
			for _, prefix := range frontendEnvPrefixes {
				if strings.HasPrefix(env.Name, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func hasFrontendHint(values ...string) bool {
	for _, token := range tokenizeHints(values...) {
		if frontendHintTokens[token] {
			return true
		}
	}
	return false
}

func tokenizeHints(values ...string) []string {
	var out []string
	for _, value := range values {
		out = append(out, strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})...)
	}
	return out
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

		ingresses, err := core.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, ing := range ingresses.Items {
				if len(ing.Spec.Rules) == 0 && ing.Spec.DefaultBackend != nil {
					result.IngressHostCount++
					result.HasFrontendSurface = true
					continue
				}
				for _, rule := range ing.Spec.Rules {
					if rule.Host == "" {
						continue
					}
					result.IngressHostCount++
					result.HasFrontendSurface = true
				}
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
