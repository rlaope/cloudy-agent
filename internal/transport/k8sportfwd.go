package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// Forwarder is a live, in-process port-forward from localhost:LocalPort to
// the named pod's targetPort. Close() shuts it down.
//
// Forwarder is the only mechanism cloudy uses to reach K8s-internal TCP
// services (Postgres/MySQL/Redis) from a bastion host. HTTP services use the
// services/proxy URL via ServiceProxy instead.
type Forwarder struct {
	stopCh    chan struct{}
	readyCh   chan struct{}
	localPort int
	err       error
}

// OpenPortForward starts a port-forward to (namespace, pod, podPort). The
// local port is chosen by the OS (port 0). The caller should defer Close().
// Blocks until the forward is ready or the context expires.
func OpenPortForward(ctx context.Context, cfg *rest.Config, namespace, pod string, podPort int, errOut io.Writer) (*Forwarder, error) {
	if errOut == nil {
		errOut = io.Discard
	}

	// Build the pod portforward subresource URL.
	base, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("transport: parse apiserver host %q: %w", cfg.Host, err)
	}
	apiURL := base.JoinPath("/api/v1/namespaces", namespace, "pods", pod, "portforward")

	// Build the SPDY round-tripper and dialer.
	//
	// SECURITY NOTE (audited 2026-05): spdy.RoundTripperFor builds its
	// own *http.Transport from cfg WITHOUT consulting cfg.WrapTransport,
	// so the POST below is the ONE documented exception to the read-only
	// HTTP contract. The exception is bounded by:
	//
	//   1. The POST only opens the SPDY upgrade handshake. After upgrade,
	//      the wire is raw TCP multiplexed by SPDY — there is no HTTP
	//      method on the bytes to method-check.
	//   2. The pods/portforward: create RBAC verb is the cluster-side
	//      fence. The apiserver itself refuses the POST when the SA
	//      lacks the verb.
	//   3. No LLM-reachable tool dispatches OpenPortForward. The single
	//      production caller is internal/tools/db/client.go for local-
	//      loopback DB driver setup; target pod is resolved internally
	//      from operator-supplied config, never from an LLM arg.
	//
	// The call-site set is locked in by k8sportfwd_callers_test.go — a
	// new caller added without updating that allow-list fails the build.
	// See docs/SAFETY.md "Documented exception: SPDY portforward upgrade".
	rt, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("transport: build SPDY round-tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, apiURL)

	// OS picks the local port; we map it to the target pod port.
	ports := []string{fmt.Sprintf("0:%d", podPort)}

	f := &Forwarder{
		stopCh:  make(chan struct{}),
		readyCh: make(chan struct{}),
	}

	pf, err := portforward.New(dialer, ports, f.stopCh, f.readyCh, io.Discard, errOut)
	if err != nil {
		return nil, fmt.Errorf("transport: create port-forwarder: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pf.ForwardPorts()
	}()

	// Wait until ready or context cancelled.
	select {
	case <-f.readyCh:
		// Forward is live; resolve the OS-assigned local port.
		fwdPorts, err := pf.GetPorts()
		if err != nil {
			close(f.stopCh)
			return nil, fmt.Errorf("transport: get forwarded ports: %w", err)
		}
		if len(fwdPorts) == 0 {
			close(f.stopCh)
			return nil, fmt.Errorf("transport: port-forwarder returned no ports")
		}
		f.localPort = int(fwdPorts[0].Local)
		return f, nil

	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("transport: port-forward failed: %w", err)
		}
		return nil, fmt.Errorf("transport: port-forward exited before ready")

	case <-ctx.Done():
		close(f.stopCh)
		return nil, ctx.Err()
	}
}

// Local returns the live "127.0.0.1:N" address the caller dials.
func (f *Forwarder) Local() string { return fmt.Sprintf("127.0.0.1:%d", f.localPort) }

// LocalPort returns the OS-assigned local port.
func (f *Forwarder) LocalPort() int { return f.localPort }

// Close stops the forwarder. Idempotent.
func (f *Forwarder) Close() error {
	select {
	case <-f.stopCh:
		// already closed
	default:
		close(f.stopCh)
	}
	return nil
}

// SelectPod returns the name of one Ready pod that matches svc.Spec.Selector.
// Returns an error if the service has no selector (headless or ExternalName)
// or no ready pods exist.
func SelectPod(ctx context.Context, kube kubernetes.Interface, namespace, svcName string) (podName string, err error) {
	svc, err := kube.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("transport: get service %s/%s: %w", namespace, svcName, err)
	}

	if len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("transport: service %s/%s has no selector", namespace, svcName)
	}

	selector := labels.SelectorFromSet(svc.Spec.Selector).String()
	podList, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("transport: list pods for service %s/%s: %w", namespace, svcName, err)
	}

	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return p.Name, nil
			}
		}
	}

	return "", fmt.Errorf("transport: no ready pods found for service %s/%s", namespace, svcName)
}
