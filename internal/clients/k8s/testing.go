package k8sclient

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	fakemetrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// NewTestClient constructs a Client from pre-built fake interfaces. Used by
// tests across the cloudy codebase (tools/k8s, tools/db, tools/log, …);
// lives in a non-_test file so external test packages can reach it.
func NewTestClient(core kubernetes.Interface, mc fakemetrics.Interface) *Client {
	return newTestClient(core, mc)
}

// NewTestClientWithDyn is the dynamic-aware variant of NewTestClient for tests
// that exercise the CRD-generic readers backed by dynamic/fake.
func NewTestClientWithDyn(core kubernetes.Interface, mc fakemetrics.Interface, dyn dynamic.Interface) *Client {
	return newTestClientWithDyn(core, mc, dyn)
}
