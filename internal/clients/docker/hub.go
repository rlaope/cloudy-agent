package dockerclient

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rlaope/cloudy/internal/config"
)

// Hub holds one read-only Client per configured Docker host, with one
// designated default. It mirrors k8sclient.Hub: callers address a host by name
// on each call, and an empty name resolves to the default (first configured
// host). Clients are built lazily on first Get so an unreachable daemon does
// not fail process startup.
type Hub struct {
	mu       sync.Mutex
	hosts    map[string]string // name -> endpoint
	clients  map[string]ReadOnlyAPI
	defaultN string
}

// NewHub constructs a Hub from the configured Docker hosts. The first entry is
// treated as the default for calls that omit the host name. Duplicate or
// empty-named entries are skipped. An empty hosts slice yields a Hub whose Get
// always errors "no docker hosts configured" — callers gate on len first.
func NewHub(hosts []config.DockerHost) (*Hub, error) {
	h := &Hub{
		hosts:   make(map[string]string, len(hosts)),
		clients: make(map[string]ReadOnlyAPI, len(hosts)),
	}
	for _, dh := range hosts {
		if dh.Name == "" {
			continue
		}
		if _, dup := h.hosts[dh.Name]; dup {
			continue
		}
		h.hosts[dh.Name] = dh.Host
		if h.defaultN == "" {
			h.defaultN = dh.Name
		}
	}
	return h, nil
}

// Default returns the name of the default host, or "" when none are configured.
func (h *Hub) Default() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.defaultN
}

// Names returns every configured host name in stable alphabetical order.
func (h *Hub) Names() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	out := make([]string, 0, len(h.hosts))
	for n := range h.hosts {
		out = append(out, n)
	}
	h.mu.Unlock()
	sort.Strings(out)
	return out
}

// Get returns the ReadOnlyAPI for the named host. An empty name resolves to
// Default(). The first call for a host builds its client lazily.
func (h *Hub) Get(name string) (ReadOnlyAPI, error) {
	if h == nil {
		return nil, fmt.Errorf("docker: hub is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if name == "" {
		name = h.defaultN
	}
	if name == "" {
		return nil, fmt.Errorf("docker: no docker hosts configured")
	}
	endpoint, ok := h.hosts[name]
	if !ok {
		return nil, fmt.Errorf("docker: host %q is not configured", name)
	}
	if c, ok := h.clients[name]; ok {
		return c, nil
	}
	c, err := NewClient(endpoint)
	if err != nil {
		return nil, fmt.Errorf("docker: build client for host %q: %w", name, err)
	}
	h.clients[name] = c
	return c, nil
}
