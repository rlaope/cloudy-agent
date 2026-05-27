package perf

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
)

// SetListPerfServicesFn replaces the package-level seam used by the detector to
// list services. Call the returned restore func in test cleanup to reset it.
func SetListPerfServicesFn(fn func(ctx context.Context, client *k8sclient.Client) (*corev1.ServiceList, error)) (restore func()) {
	orig := listPerfServicesFn
	listPerfServicesFn = fn
	return func() { listPerfServicesFn = orig }
}
