package setup

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"k8s.io/client-go/tools/clientcmd"
)

// Check represents a single health-check result produced by Doctor.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Doctor runs a set of structured health checks and returns their results.
// The output is purely structured; the CLI layer is responsible for formatting
// it for human or JSON consumption.
func Doctor(ctx context.Context, opts Options) ([]Check, error) {
	var checks []Check

	// 1. Kubeconfig parseable. Validate the Kubernetes kubeconfig (NOT
	// cloudy's config.yaml) — empty path falls back to clientcmd defaults.
	checks = append(checks, checkKubeconfig(opts.KubeconfigPath))

	// 2. Profile file exists and is valid (reuse EnsureReady logic).
	gateResult, err := EnsureReady(ctx, ModeOneShot, opts)
	if err != nil {
		return nil, fmt.Errorf("doctor: gate check failed: %w", err)
	}

	profileOK := gateResult.State == StateReady || gateResult.State == StatePartial
	profileDetail := "profile is valid and fresh"
	if !profileOK {
		profileDetail = strings.Join(gateResult.Reasons, "; ")
	}
	checks = append(checks, Check{
		Name:   "profile valid",
		OK:     profileOK,
		Detail: profileDetail,
	})

	// 3. Active context reachable (from profile data, not a live probe).
	checks = append(checks, checkContextReachable(opts.ProfilePath))

	// 4. Default model has an API key in the environment.
	checks = append(checks, checkModelAPIKey(opts.ConfigPath))

	return checks, nil
}

// checkKubeconfig verifies that the kubeconfig can be parsed.
func checkKubeconfig(configPath string) Check {
	// Use clientcmd default loading rules when no explicit path is given.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if configPath != "" {
		rules.ExplicitPath = configPath
	}
	_, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{},
	).RawConfig()
	if err != nil {
		return Check{
			Name:   "kubeconfig parseable",
			OK:     false,
			Detail: err.Error(),
		}
	}
	return Check{Name: "kubeconfig parseable", OK: true, Detail: "kubeconfig loaded successfully"}
}

// checkContextReachable inspects the saved profile for reachability data.
func checkContextReachable(profilePath string) Check {
	profile, err := config.LoadProfile(profilePath)
	if err != nil || !profile.IsValid() {
		return Check{
			Name:   "active context reachable",
			OK:     false,
			Detail: "profile unavailable — cannot determine reachability",
		}
	}
	for _, cp := range profile.Contexts {
		if cp.Reachable {
			return Check{
				Name:   "active context reachable",
				OK:     true,
				Detail: fmt.Sprintf("context %q was reachable at last scan", cp.Name),
			}
		}
	}
	return Check{
		Name:   "active context reachable",
		OK:     false,
		Detail: "no context was reachable at last scan — re-run 'cloudy init'",
	}
}

// checkModelAPIKey verifies that the default model's provider has an API key
// set in the environment. It loads config to discover the provider and the
// expected env var name.
func checkModelAPIKey(configPath string) Check {
	cfg, err := config.Load(configPath)
	if err != nil {
		return Check{
			Name:   "default model has API key in env",
			OK:     false,
			Detail: fmt.Sprintf("cannot load config: %v", err),
		}
	}

	// Determine the provider from the default model name.
	provider := guessProvider(cfg.DefaultModel)
	pc, ok := cfg.Providers[provider]
	if !ok || pc.APIKeyEnv == "" {
		// Try well-known env vars by convention.
		envVar := wellKnownEnvVar(provider)
		if envVar == "" {
			return Check{
				Name:   "default model has API key in env",
				OK:     false,
				Detail: fmt.Sprintf("no API key env var configured for provider %q", provider),
			}
		}
		pc.APIKeyEnv = envVar
	}

	val := os.Getenv(pc.APIKeyEnv)
	if val == "" {
		return Check{
			Name:   "default model has API key in env",
			OK:     false,
			Detail: fmt.Sprintf("env var %s is not set", pc.APIKeyEnv),
		}
	}
	return Check{
		Name:   "default model has API key in env",
		OK:     true,
		Detail: fmt.Sprintf("env var %s is set", pc.APIKeyEnv),
	}
}

// guessProvider maps a model identifier to a provider name.
func guessProvider(model string) string {
	model = strings.ToLower(model)
	switch {
	case strings.HasPrefix(model, "claude"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3"):
		return "openai"
	case strings.HasPrefix(model, "gemini"):
		return "google"
	default:
		return "anthropic"
	}
}

// wellKnownEnvVar returns the conventional API key env var for a provider.
func wellKnownEnvVar(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	default:
		return ""
	}
}
