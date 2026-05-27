package k8sclient

import (
	"fmt"
	"sort"
	"sync"
)

// NamespaceChecker is the optional callback the wiring layer installs on a Hub
// so that K8s tools can ask "is this namespace permitted by the active
// Permission Profile?" without importing the permission package directly
// (which would create an import cycle: k8s -> permission -> tools -> k8s).
//
// A nil checker means "no narrowing"; an empty namespace ("" = all namespaces)
// is intentionally NOT short-circuited here — the checker decides.
type NamespaceChecker func(ns string) error

// Hub holds one *Client per kubeconfig context, with one designated default.
//
// Multi-context cloudy lets the LLM address several clusters in one process
// by passing an optional `context` argument on each tool call. When the
// context list is empty, Hub holds a single client built from the kubeconfig's
// current-context — preserving v0.1 single-cluster behaviour.
type Hub struct {
	kubeconfigPath string

	mu         sync.Mutex
	clients    map[string]*Client
	defaultCtx string
	contextSet map[string]struct{}

	checkNS NamespaceChecker
}

// NewHub constructs a Hub. When contexts is empty, the kubeconfig's
// current-context is used as the single, default context. When contexts is
// non-empty, one client is built per name (lazy init OK), with the first
// entry treated as the default for calls that omit the `context` arg.
func NewHub(kubeconfigPath string, contexts []string) (*Hub, error) {
	h := &Hub{
		kubeconfigPath: kubeconfigPath,
		clients:        make(map[string]*Client),
		contextSet:     make(map[string]struct{}),
	}
	if len(contexts) == 0 {
		// Single-context mode: build the client backed by current-context now
		// so that any kubeconfig errors surface at startup, matching v0.1.
		c, err := NewClient(kubeconfigPath, "")
		if err != nil {
			return nil, err
		}
		h.clients[""] = c
		h.contextSet[""] = struct{}{}
		// defaultCtx stays "" so Get("") and Default() resolve consistently.
		return h, nil
	}
	for _, name := range contexts {
		if name == "" {
			continue
		}
		h.contextSet[name] = struct{}{}
	}
	if len(h.contextSet) == 0 {
		// All entries were empty strings; treat as single-context mode.
		c, err := NewClient(kubeconfigPath, "")
		if err != nil {
			return nil, err
		}
		h.clients[""] = c
		h.contextSet[""] = struct{}{}
		return h, nil
	}
	// Pick the first non-empty context as the default.
	for _, name := range contexts {
		if name != "" {
			h.defaultCtx = name
			break
		}
	}
	return h, nil
}

// NewHubFromClients is a test helper that constructs a Hub from pre-built
// clients keyed by context name. The first key in the sorted Names() set is
// the default unless defaultName is non-empty.
func NewHubFromClients(clients map[string]*Client, defaultName string) *Hub {
	h := &Hub{
		clients:    make(map[string]*Client, len(clients)),
		contextSet: make(map[string]struct{}, len(clients)),
		defaultCtx: defaultName,
	}
	for name, c := range clients {
		h.clients[name] = c
		h.contextSet[name] = struct{}{}
	}
	if h.defaultCtx == "" {
		// Pick alpha-first name as default for stability.
		names := h.Names()
		if len(names) > 0 {
			h.defaultCtx = names[0]
		}
	}
	return h
}

// Default returns the name of the default context. Empty string means
// "kubeconfig current-context" (single-context mode).
func (h *Hub) Default() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.defaultCtx
}

// Names returns every configured context name in stable alphabetical order.
func (h *Hub) Names() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	out := make([]string, 0, len(h.contextSet))
	for n := range h.contextSet {
		out = append(out, n)
	}
	h.mu.Unlock()
	sort.Strings(out)
	return out
}

// Get returns the *Client for the named context. An empty name resolves to
// Default(). The first call for a multi-context name builds the client lazily.
func (h *Hub) Get(name string) (*Client, error) {
	if h == nil {
		return nil, fmt.Errorf("k8s: hub is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if name == "" {
		name = h.defaultCtx
	}
	if _, ok := h.contextSet[name]; !ok {
		return nil, fmt.Errorf("k8s: context %q is not configured", name)
	}
	if c, ok := h.clients[name]; ok {
		return c, nil
	}
	// Lazy build: pass the explicit context name to clientcmd.
	c, err := NewClient(h.kubeconfigPath, name)
	if err != nil {
		return nil, fmt.Errorf("k8s: build client for context %q: %w", name, err)
	}
	h.clients[name] = c
	return c, nil
}

// WithNamespaceChecker installs the namespace-allow-list callback. Returns
// the Hub for chaining. Pass nil to clear.
func (h *Hub) WithNamespaceChecker(fn NamespaceChecker) *Hub {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	h.checkNS = fn
	h.mu.Unlock()
	return h
}

// CheckNamespace runs the installed checker (if any). Returns nil when no
// checker is installed.
func (h *Hub) CheckNamespace(ns string) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	fn := h.checkNS
	h.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ns)
}

// MultiContext reports whether the hub was configured with more than one
// context. Tools use this to decide whether to render a "CONTEXT" column.
func (h *Hub) MultiContext() bool {
	if h == nil {
		return false
	}
	return len(h.Names()) > 1
}
