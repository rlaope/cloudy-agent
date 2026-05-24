package k8s

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// NewTestClient exposes the internal newTestClient constructor for use in
// external test packages (_test suffix).
func NewTestClient(core kubernetes.Interface, mc fakemetrics.Interface) *Client {
	return newTestClient(core, mc)
}

// NewTestClientWithDyn exposes newTestClientWithDyn for external tests that
// exercise the CRD-generic readers backed by dynamic/fake.
func NewTestClientWithDyn(core kubernetes.Interface, mc fakemetrics.Interface, dyn dynamic.Interface) *Client {
	return newTestClientWithDyn(core, mc, dyn)
}
