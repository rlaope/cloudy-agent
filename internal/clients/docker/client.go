// Package dockerclient is the shared read-only Docker adapter used by the
// change-source that reports recent container/image changes per host. It
// mirrors the k8sclient.Client / Hub shape: a Client wraps one daemon, a Hub
// manages several daemons by name with lazy init.
//
// Every method here is list/inspect only. cloudy never starts, stops,
// creates, or removes containers — the ReadOnlyAPI interface deliberately
// exposes no mutating method, so the read-only contract holds by construction.
package dockerclient

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockersdk "github.com/docker/docker/client"
)

// ReadOnlyAPI is the minimal read surface the change-source consumes from a
// Docker daemon. It is intentionally small so it can be mocked in tests, and
// intentionally read-only so no mutating call can leak in via this seam.
type ReadOnlyAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
}

// Client is a read-only façade over one Docker daemon. It satisfies
// ReadOnlyAPI by delegating to the embedded SDK client.
type Client struct {
	sdk *dockersdk.Client
}

// NewClient builds a Client for the daemon at host (e.g.
// "unix:///var/run/docker.sock" or "tcp://host:2375"). An empty host falls
// back to the SDK's environment defaults (DOCKER_HOST etc.). API version is
// negotiated with the daemon so the client works across daemon versions.
func NewClient(host string) (*Client, error) {
	opts := []dockersdk.Opt{dockersdk.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, dockersdk.WithHost(host))
	} else {
		opts = append(opts, dockersdk.FromEnv)
	}
	sdk, err := dockersdk.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: build client: %w", err)
	}
	return &Client{sdk: sdk}, nil
}

// ContainerList lists containers on the daemon.
func (c *Client) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	return c.sdk.ContainerList(ctx, options)
}

// ContainerInspect returns the detailed state of a single container.
func (c *Client) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	return c.sdk.ContainerInspect(ctx, containerID)
}

// ImageList lists images on the daemon.
func (c *Client) ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error) {
	return c.sdk.ImageList(ctx, options)
}
