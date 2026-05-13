package k8s

import (
	"k8s.io/client-go/kubernetes"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// NewTestClient exposes the internal newTestClient constructor for use in
// external test packages (_test suffix).
func NewTestClient(core kubernetes.Interface, mc fakemetrics.Interface) *Client {
	return newTestClient(core, mc)
}
