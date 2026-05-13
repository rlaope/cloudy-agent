package wiring

import (
	"fmt"
	"os"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"

	// Register all provider adapters via side-effect imports.
	_ "github.com/rlaope/cloudy/internal/llm/anthropic"
	_ "github.com/rlaope/cloudy/internal/llm/google"
	_ "github.com/rlaope/cloudy/internal/llm/moonshot"
	_ "github.com/rlaope/cloudy/internal/llm/openai"
	_ "github.com/rlaope/cloudy/internal/llm/openai_compat"
)

// ErrMissingKey is returned by BuildProvider when the required API key
// environment variable is absent for the resolved provider.
type ErrMissingKey struct {
	Provider string
	EnvVar   string
}

func (e *ErrMissingKey) Error() string {
	return fmt.Sprintf("wiring: provider %q requires env var %s (not set)", e.Provider, e.EnvVar)
}

// BuildProvider resolves model → (Provider, modelID) and validates that the
// relevant API-key environment variable is present.
func BuildProvider(model string) (llm.Provider, string, error) {
	if model == "" {
		return nil, "", fmt.Errorf("wiring: model identifier is empty — run `cloudy setup` to configure one")
	}

	provider, modelID, err := llm.Resolve(model)
	if err != nil {
		return nil, "", fmt.Errorf("wiring: resolve model %q: %w", model, err)
	}

	// Validate API key presence for well-known providers.
	envVar := wellKnownKeyEnv(provider.Name())
	if envVar != "" && os.Getenv(envVar) == "" {
		return nil, "", &ErrMissingKey{Provider: provider.Name(), EnvVar: envVar}
	}

	return provider, modelID, nil
}

// wellKnownKeyEnv maps a provider name to its conventional API-key env var.
// Returns "" for providers that don't require an API key check at wiring time
// (e.g. local/openai_compat).
func wellKnownKeyEnv(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	case "moonshot":
		return "MOONSHOT_API_KEY"
	default:
		return ""
	}
}
