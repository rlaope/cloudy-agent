package wiring_test

import (
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/wiring"
)

// TestRebuild_InstallsActiveRegistry pins the orchestrator contract: a
// successful Rebuild call swaps the package-global active registry so
// callers no longer need to remember to call wiring.Replace themselves.
// Pre-extraction, the three callsites that built a registry each had to
// remember the Replace step — the next reload site that forgot it
// would have left wiring.Current returning a stale registry.
func TestRebuild_InstallsActiveRegistry(t *testing.T) {
	// Clear any previous test's residue.
	wiring.Replace(nil)
	defer wiring.Replace(nil)

	cfg := config.Default()
	reg, _ := wiring.Rebuild(cfg, wiring.RebuildOpts{})
	if reg == nil {
		t.Fatal("Rebuild returned nil registry on default config")
	}
	if got := wiring.Current(); got != reg {
		t.Errorf("Rebuild did not install registry as active; want %p got %p", reg, got)
	}
}
