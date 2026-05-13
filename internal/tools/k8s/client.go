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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	return &Client{core: core, metrics: mc}, nil
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
	return &Client{core: core, metrics: mc}, nil
}

// newTestClient constructs a Client directly from pre-built interfaces (for
// unit tests that supply fake clientsets).
func newTestClient(core kubernetes.Interface, mc metricsclient.Interface) *Client {
	return &Client{core: core, metrics: mc}
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

// Deployments lists deployments in the given namespace.
func (c *Client) Deployments(ns string) (*appsv1.DeploymentList, error) {
	return c.core.AppsV1().Deployments(ns).List(context.Background(), metav1.ListOptions{})
}

// StatefulSets lists stateful sets in the given namespace.
func (c *Client) StatefulSets(ns string) (*appsv1.StatefulSetList, error) {
	return c.core.AppsV1().StatefulSets(ns).List(context.Background(), metav1.ListOptions{})
}

// DaemonSets lists daemon sets in the given namespace.
func (c *Client) DaemonSets(ns string) (*appsv1.DaemonSetList, error) {
	return c.core.AppsV1().DaemonSets(ns).List(context.Background(), metav1.ListOptions{})
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
