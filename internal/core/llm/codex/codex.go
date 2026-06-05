// Package codex implements an llm.Provider adapter for Codex model routing.
//
// The transport intentionally reuses Cloudy's OpenAI Chat Completions-compatible
// streaming implementation so tool calls keep working exactly like the OpenAI
// provider path. Select it with model ids in the form "codex/<model-id>".
//
// Configuration (environment variables):
//
//	CODEX_API_KEY      – required
//	CODEX_BASE_URL     – optional; defaults to https://api.openai.com
package codex

import (
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/llm/openai"
)

const (
	providerName   = "codex"
	apiKeyEnv      = "CODEX_API_KEY"
	baseURLEnv     = "CODEX_BASE_URL"
	defaultBaseURL = "https://api.openai.com"
)

func init() {
	llm.Register(New())
}

// New returns a Codex provider. The API key is read lazily from CODEX_API_KEY
// on every Stream call so /login can save the key after package init.
func New() llm.Provider {
	return openai.NewWithOptions(openai.Options{
		Name:           providerName,
		APIKeyEnv:      apiKeyEnv,
		BaseURLEnv:     baseURLEnv,
		DefaultBaseURL: defaultBaseURL,
	})
}
