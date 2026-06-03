package setup

import (
	"context"
	"fmt"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

// Mode describes the execution surface that called EnsureReady.
type Mode int

const (
	ModeTUI     Mode = iota // interactive bubbletea UI
	ModeOneShot             // non-interactive single command
	ModeSkill               // skill execution path
)

// State is the readiness outcome returned by EnsureReady.
type State int

const (
	StateReady      State = iota // profile is complete and fresh
	StateNeedsSetup              // profile is missing, stale, or invalid
	StatePartial                 // profile is valid but some required components are absent
)

// Result is the structured outcome of EnsureReady.
type Result struct {
	State    State
	Reasons  []string // human-readable explanation(s) for non-Ready states
	Disabled []string // tool names to disable on StatePartial
}

// Options configures the EnsureReady gate.
type Options struct {
	// ConfigPath is the path to config.yaml (used for future checks).
	ConfigPath string

	// KubeconfigPath is the Kubernetes kubeconfig to validate (the
	// --kubeconfig flag). Empty falls back to clientcmd's defaults
	// (KUBECONFIG / ~/.kube/config). This is NOT cloudy's config.yaml.
	KubeconfigPath string

	// ProfilePath is the path to profile.yaml.
	ProfilePath string

	// ProfileTTL is the maximum age of a profile before it is considered stale.
	// Zero uses the default of 7 × 24 h.
	ProfileTTL time.Duration

	// RequiredComponents lists component names that must be present for full
	// readiness. Supported values: "prometheus".
	RequiredComponents []string
}

// defaultProfileTTL is used when Options.ProfileTTL is zero.
const defaultProfileTTL = 7 * 24 * time.Hour

// EnsureReady checks whether cloudy is configured and ready to run.
// It does NOT trigger setup itself — callers inspect the returned Result and
// decide what surface to present (wizard, error message, etc.).
func EnsureReady(_ context.Context, _ Mode, opts Options) (Result, error) {
	ttl := opts.ProfileTTL
	if ttl == 0 {
		ttl = defaultProfileTTL
	}

	profile, err := config.LoadProfile(opts.ProfilePath)
	if err != nil {
		return Result{
			State:   StateNeedsSetup,
			Reasons: []string{fmt.Sprintf("profile unreadable: %v", err)},
		}, nil
	}

	// Profile file does not exist yet (LoadProfile returns zero value + nil error).
	if profile.SchemaVersion == 0 && len(profile.Contexts) == 0 {
		return Result{
			State:   StateNeedsSetup,
			Reasons: []string{"no profile found — run 'cloudy setup' to set up"},
		}, nil
	}

	if profile.SchemaVersion != config.CurrentSchemaVersion {
		return Result{
			State: StateNeedsSetup,
			Reasons: []string{fmt.Sprintf(
				"profile schema version %d is outdated (want %d) — re-run 'cloudy setup'",
				profile.SchemaVersion, config.CurrentSchemaVersion,
			)},
		}, nil
	}

	if profile.Expired(ttl) {
		return Result{
			State:   StateNeedsSetup,
			Reasons: []string{"profile is stale — re-run 'cloudy setup' to refresh"},
		}, nil
	}

	if !profile.IsValid() {
		return Result{
			State:   StateNeedsSetup,
			Reasons: []string{"profile contains no context entries — re-run 'cloudy setup'"},
		}, nil
	}

	// Check optional required components.
	var partialReasons []string
	var disabled []string

	for _, comp := range opts.RequiredComponents {
		switch comp {
		case "prometheus":
			found := false
			for _, cp := range profile.Contexts {
				if cp.HasPrometheus {
					found = true
					break
				}
			}
			if !found {
				partialReasons = append(partialReasons,
					"prometheus not detected in any context — prom-explorer skill disabled")
				disabled = append(disabled, "prom-explorer")
			}
		}
	}

	if len(partialReasons) > 0 {
		return Result{
			State:    StatePartial,
			Reasons:  partialReasons,
			Disabled: disabled,
		}, nil
	}

	return Result{State: StateReady}, nil
}
