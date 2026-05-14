package discovery

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// defaultRunDeadline is the fallback deadline applied to Run when the caller
// has not supplied one on ctx. Discovery is best-effort; long-running probes
// are bounded so /setup stays responsive.
const defaultRunDeadline = 30 * time.Second

// Detector is the contract backend packages implement to contribute Findings.
// Implementations live under internal/tools/<kind>/ and register themselves
// from init() so the coordinator never has to know about specific backends.
type Detector interface {
	Name() string // unique stable id, e.g. "tools.prom"
	Detect(ctx context.Context, env Env) []Finding
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Detector{}
)

// Register adds d to the global detector list. Intended for init() use;
// panics on duplicate Name() to surface mistakes at startup.
func Register(d Detector) {
	if d == nil {
		panic("discovery: Register called with nil Detector")
	}
	name := d.Name()
	if name == "" {
		panic("discovery: Detector.Name() must be non-empty")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("discovery: duplicate Detector name %q", name))
	}
	registry[name] = d
}

// All returns a snapshot of every registered Detector, sorted by Name().
func All() []Detector {
	registryMu.RLock()
	out := make([]Detector, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	registryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Reset clears the registry. For tests only.
func Reset() {
	registryMu.Lock()
	registry = map[string]Detector{}
	registryMu.Unlock()
}

// Run fan-outs every registered Detector against env with a 30s default
// deadline (overridable via ctx). Findings are aggregated and stable-sorted:
//  1. by Group
//  2. by Kind
//  3. by Source.Context, Source.Namespace, Source.ServiceName, Source.ExternalURL
//
// Detector.Detect calls that panic are recovered and treated as zero
// findings. Errors are intentionally not returned — Findings are best-effort.
func Run(ctx context.Context, env Env) []Finding {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRunDeadline)
		defer cancel()
	}

	detectors := All()
	if len(detectors) == 0 {
		return nil
	}

	results := make(chan []Finding, len(detectors))
	var wg sync.WaitGroup
	for _, d := range detectors {
		wg.Add(1)
		go func(d Detector) {
			defer wg.Done()
			defer func() {
				// Recover so one buggy Detector cannot crash /setup. The
				// recovered value is intentionally discarded; surfacing it
				// would require an error channel and Run is best-effort.
				if r := recover(); r != nil {
					_ = r
				}
			}()
			results <- d.Detect(ctx, env)
		}(d)
	}

	wg.Wait()
	close(results)

	var all []Finding
	for fs := range results {
		all = append(all, fs...)
	}

	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source.Context != b.Source.Context {
			return a.Source.Context < b.Source.Context
		}
		if a.Source.Namespace != b.Source.Namespace {
			return a.Source.Namespace < b.Source.Namespace
		}
		if a.Source.ServiceName != b.Source.ServiceName {
			return a.Source.ServiceName < b.Source.ServiceName
		}
		return a.Source.ExternalURL < b.Source.ExternalURL
	})
	return all
}
