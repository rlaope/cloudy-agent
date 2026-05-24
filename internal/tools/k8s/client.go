// Package k8s provides read-only Kubernetes tools for the cloudy SRE agent.
//
// All HTTP traffic from this package flows through transport.Wrap so that the
// read-only contract (GET/HEAD/OPTIONS only) is enforced at the transport
// layer, in addition to the RBAC ClusterRole guard on the cluster.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/rlaope/cloudy/internal/transport"
)

// ErrMetricsUnavailable is returned by TopPods / TopNodes when the
// metrics-server is not installed or not reachable in the cluster.
var ErrMetricsUnavailable = errors.New("k8s: metrics-server unavailable")

// MetricsPod holds resource usage for a single pod.
type MetricsPod struct {
	Namespace string
	Name      string
	// CPU usage in milli-cores.
	CPUMillis int64
	// Memory usage in bytes.
	MemoryBytes int64
}

// MetricsNode holds resource usage for a single node.
type MetricsNode struct {
	Name string
	// CPU usage in milli-cores.
	CPUMillis int64
	// Memory usage in bytes.
	MemoryBytes int64
}

// Client is a read-only façade over the Kubernetes API. It exposes only
// list/get operations and wraps every outbound request with
// transport.ReadOnlyRoundTripper.
type Client struct {
	core    kubernetes.Interface
	metrics metricsclient.Interface
	// dyn is the dynamic client used by the CRD-generic readers
	// (k8s.list_crds, k8s.list_cr). It honours the same cfg.WrapTransport
	// the typed clients use, so the read-only contract is preserved. nil
	// when the Client was built via newTestClient without a dynamic stub.
	dyn dynamic.Interface
	// restCfg is the rest.Config the production clients were built from. It
	// is retained so callers (e.g. the wiring layer building a ServiceProxy
	// for discovery) can reuse the apiserver auth without re-loading the
	// kubeconfig. Tests that inject pre-built interfaces leave this nil;
	// RESTConfig() returns nil in that case.
	restCfg *rest.Config
}

// NewClient builds a Client. If kubeconfigPath is empty and
// ~/.kube/config does not exist, in-cluster config is attempted.
// The rest.Config's WrapTransport is set to transport.Wrap so every
// API call is method-checked.
func NewClient(kubeconfigPath, kubeContext string) (*Client, error) {
	cfg, err := loadConfig(kubeconfigPath, kubeContext)
	if err != nil {
		return nil, fmt.Errorf("k8s: load config: %w", err)
	}
	// Enforce read-only at the transport layer.
	cfg.WrapTransport = transport.Wrap

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build core client: %w", err)
	}
	mc, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build metrics client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build dynamic client: %w", err)
	}
	return &Client{core: core, metrics: mc, dyn: dyn, restCfg: cfg}, nil
}

// newClientFromConfig is used in tests to inject a pre-built config.
func newClientFromConfig(cfg *rest.Config) (*Client, error) {
	cfg.WrapTransport = transport.Wrap
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mc, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{core: core, metrics: mc, dyn: dyn, restCfg: cfg}, nil
}

// newTestClient constructs a Client directly from pre-built interfaces (for
// unit tests that supply fake clientsets). The dynamic client is left nil;
// tests that exercise the CRD-generic readers should use newTestClientWithDyn.
func newTestClient(core kubernetes.Interface, mc metricsclient.Interface) *Client {
	return &Client{core: core, metrics: mc}
}

// newTestClientWithDyn is the dynamic-aware variant of newTestClient for unit
// tests that drive k8s.list_crds / k8s.list_cr against dynamic/fake.
func newTestClientWithDyn(core kubernetes.Interface, mc metricsclient.Interface, dyn dynamic.Interface) *Client {
	return &Client{core: core, metrics: mc, dyn: dyn}
}

// RESTConfig returns the *rest.Config the Client was constructed from, or nil
// when the Client was built via newTestClient (which injects fake interfaces
// rather than a real config). Callers that need an apiserver-authenticated
// HTTP transport (e.g. transport.NewServiceProxy) should check for nil first.
func (c *Client) RESTConfig() *rest.Config {
	if c == nil {
		return nil
	}
	return c.restCfg
}

// Core returns the underlying kubernetes.Interface. Used by callers that need
// direct access to the core API group (e.g. transport.SelectPod for port-forward
// target resolution).
func (c *Client) Core() kubernetes.Interface {
	if c == nil {
		return nil
	}
	return c.core
}

// Dyn returns the underlying dynamic.Interface used by the CRD-generic
// readers. Returns nil when the Client was constructed via newTestClient
// (which omits the dynamic stub) — callers must nil-check before use.
func (c *Client) Dyn() dynamic.Interface {
	if c == nil {
		return nil
	}
	return c.dyn
}

func loadConfig(kubeconfigPath, kubeContext string) (*rest.Config, error) {
	// Explicit path provided.
	if kubeconfigPath != "" {
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
			&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
		).ClientConfig()
	}

	// Check default ~/.kube/config.
	home, _ := os.UserHomeDir()
	defaultKubeconfig := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(defaultKubeconfig); err == nil {
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: defaultKubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
		).ClientConfig()
	}

	// Fall back to in-cluster config.
	return rest.InClusterConfig()
}

// Pods lists pods in the given namespace (empty string = all namespaces).
func (c *Client) Pods(ns string, opts metav1.ListOptions) (*corev1.PodList, error) {
	return c.core.CoreV1().Pods(ns).List(context.Background(), opts)
}

// Pod fetches a single pod by namespace and name.
func (c *Client) Pod(ns, name string) (*corev1.Pod, error) {
	return c.core.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
}

// Events lists events in the given namespace.
func (c *Client) Events(ns string, opts metav1.ListOptions) (*corev1.EventList, error) {
	return c.core.CoreV1().Events(ns).List(context.Background(), opts)
}

// PodLogs retrieves logs for the specified pod/container combination.
func (c *Client) PodLogs(ns, name string, opts *corev1.PodLogOptions) (string, error) {
	req := c.core.CoreV1().Pods(ns).GetLogs(name, opts)
	stream, err := req.Stream(context.Background())
	if err != nil {
		return "", fmt.Errorf("k8s: open log stream: %w", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("k8s: read log stream: %w", err)
	}
	return string(data), nil
}

// Nodes lists all cluster nodes.
func (c *Client) Nodes() (*corev1.NodeList, error) {
	return c.core.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
}

// Namespaces lists all namespaces.
func (c *Client) Namespaces() (*corev1.NamespaceList, error) {
	return c.core.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
}

// Services lists services in the given namespace (empty string = all namespaces).
func (c *Client) Services(ctx context.Context, ns string, opts metav1.ListOptions) (*corev1.ServiceList, error) {
	return c.core.CoreV1().Services(ns).List(ctx, opts)
}

// Deployments lists deployments in the given namespace.
func (c *Client) Deployments(ns string, opts metav1.ListOptions) (*appsv1.DeploymentList, error) {
	return c.core.AppsV1().Deployments(ns).List(context.Background(), opts)
}

// StatefulSets lists stateful sets in the given namespace.
func (c *Client) StatefulSets(ns string, opts metav1.ListOptions) (*appsv1.StatefulSetList, error) {
	return c.core.AppsV1().StatefulSets(ns).List(context.Background(), opts)
}

// DaemonSets lists daemon sets in the given namespace.
func (c *Client) DaemonSets(ns string, opts metav1.ListOptions) (*appsv1.DaemonSetList, error) {
	return c.core.AppsV1().DaemonSets(ns).List(context.Background(), opts)
}

// Jobs lists jobs in the given namespace.
func (c *Client) Jobs(ns string, opts metav1.ListOptions) (*batchv1.JobList, error) {
	return c.core.BatchV1().Jobs(ns).List(context.Background(), opts)
}

// CronJobs lists cron jobs in the given namespace.
func (c *Client) CronJobs(ns string, opts metav1.ListOptions) (*batchv1.CronJobList, error) {
	return c.core.BatchV1().CronJobs(ns).List(context.Background(), opts)
}

// ServicesList lists services in the given namespace. Distinct from Services
// (which takes a context) to fit the synchronous ListResourceSpec signature.
func (c *Client) ServicesList(ns string, opts metav1.ListOptions) (*corev1.ServiceList, error) {
	return c.core.CoreV1().Services(ns).List(context.Background(), opts)
}

// Ingresses lists ingresses in the given namespace.
func (c *Client) Ingresses(ns string, opts metav1.ListOptions) (*networkingv1.IngressList, error) {
	return c.core.NetworkingV1().Ingresses(ns).List(context.Background(), opts)
}

// HPAs lists horizontal pod autoscalers in the given namespace.
func (c *Client) HPAs(ns string, opts metav1.ListOptions) (*autoscalingv2.HorizontalPodAutoscalerList, error) {
	return c.core.AutoscalingV2().HorizontalPodAutoscalers(ns).List(context.Background(), opts)
}

// PDBs lists pod disruption budgets in the given namespace.
func (c *Client) PDBs(ns string, opts metav1.ListOptions) (*policyv1.PodDisruptionBudgetList, error) {
	return c.core.PolicyV1().PodDisruptionBudgets(ns).List(context.Background(), opts)
}

// NetworkPolicies lists network policies in the given namespace.
func (c *Client) NetworkPolicies(ns string, opts metav1.ListOptions) (*networkingv1.NetworkPolicyList, error) {
	return c.core.NetworkingV1().NetworkPolicies(ns).List(context.Background(), opts)
}

// TopPods returns per-pod CPU/memory usage. Returns ErrMetricsUnavailable if
// the metrics-server is absent.
func (c *Client) TopPods(ns string) ([]MetricsPod, error) {
	list, err := c.metrics.MetricsV1beta1().PodMetricses(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMetricsUnavailable, err)
	}
	return podMetricsToSlice(list), nil
}

// TopNodes returns per-node CPU/memory usage. Returns ErrMetricsUnavailable
// if the metrics-server is absent.
func (c *Client) TopNodes() ([]MetricsNode, error) {
	list, err := c.metrics.MetricsV1beta1().NodeMetricses().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMetricsUnavailable, err)
	}
	return nodeMetricsToSlice(list), nil
}

func podMetricsToSlice(list *metricsv1beta1.PodMetricsList) []MetricsPod {
	out := make([]MetricsPod, 0, len(list.Items))
	for _, pm := range list.Items {
		var cpuM, memB int64
		for _, c := range pm.Containers {
			cpuM += c.Usage.Cpu().MilliValue()
			memB += c.Usage.Memory().Value()
		}
		out = append(out, MetricsPod{
			Namespace:   pm.Namespace,
			Name:        pm.Name,
			CPUMillis:   cpuM,
			MemoryBytes: memB,
		})
	}
	return out
}

func nodeMetricsToSlice(list *metricsv1beta1.NodeMetricsList) []MetricsNode {
	out := make([]MetricsNode, 0, len(list.Items))
	for _, nm := range list.Items {
		out = append(out, MetricsNode{
			Name:        nm.Name,
			CPUMillis:   nm.Usage.Cpu().MilliValue(),
			MemoryBytes: nm.Usage.Memory().Value(),
		})
	}
	return out
}
