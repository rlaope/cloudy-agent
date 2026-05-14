package log

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/tools/k8s"
)

// SetListServicesFn replaces the package-level listServicesFn seam for tests.
// Returns a restore function that resets it to the original value.
func SetListServicesFn(fn func(ctx context.Context, client *k8s.Client) (*corev1.ServiceList, error)) func() {
	orig := listServicesFn
	listServicesFn = fn
	return func() { listServicesFn = orig }
}

// DefaultListOptions returns the ListOptions used by defaultListServices so
// tests can assert the Limit without re-hardcoding it.
func DefaultListOptions() metav1.ListOptions { return metav1.ListOptions{Limit: 500} }
