package wiring

import (
	"sync/atomic"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// active holds the currently-installed Registry. Updated atomically by
// Replace(); read by Current(). The zero value is a nil *Registry — callers
// must handle that (e.g. by building one before first read).
var active atomic.Pointer[tools.Registry]

// Current returns the currently-installed Registry, or nil if none has been
// installed yet. The returned pointer is safe to use concurrently with calls
// to Replace — in-flight callers continue to operate on the snapshot they
// received here.
func Current() *tools.Registry { return active.Load() }

// Replace atomically swaps the installed Registry. Pass a freshly built
// Registry; do not mutate it after publication. A nil argument is allowed and
// causes Current() to return nil — useful for tests that want to clear state.
func Replace(r *tools.Registry) { active.Store(r) }
