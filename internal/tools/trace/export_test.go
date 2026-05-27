package trace

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"
)

// SetListServicesFn replaces the package-level listServicesFn seam for tests.
// Returns a restore function that resets it to the original value.
func SetListServicesFn(fn func(ctx context.Context, client *k8sclient.Client) (*corev1.ServiceList, error)) func() {
	orig := listServicesFn
	listServicesFn = fn
	return func() { listServicesFn = orig }
}
