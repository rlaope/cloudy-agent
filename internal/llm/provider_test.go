package llm_test

import (
	"errors"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"

	// Register all adapters so Resolve can find them.
	_ "github.com/rlaope/cloudy/internal/llm/anthropic"
	_ "github.com/rlaope/cloudy/internal/llm/google"
	_ "github.com/rlaope/cloudy/internal/llm/moonshot"
	_ "github.com/rlaope/cloudy/internal/llm/openai"
	_ "github.com/rlaope/cloudy/internal/llm/openai_compat"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		model        string
		wantProvider string
		wantModelID  string
	}{
		{"gpt-4o", "openai", "gpt-4o"},
		{"gpt-4-turbo", "openai", "gpt-4-turbo"},
		{"o1-preview", "openai", "o1-preview"},
		{"claude-3-5-sonnet-20241022", "anthropic", "claude-3-5-sonnet-20241022"},
		{"gemini-1.5-pro", "google", "gemini-1.5-pro"},
		{"kimi-latest", "moonshot", "kimi-latest"},
		{"moonshot-v1-8k", "moonshot", "moonshot-v1-8k"},
		{"local/llama3", "openai_compat", "llama3"},
		{"local/mistral:7b", "openai_compat", "mistral:7b"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.model, func(t *testing.T) {
			p, modelID, err := llm.Resolve(tc.model)
			if err != nil {
				t.Fatalf("Resolve(%q) error: %v", tc.model, err)
			}
			if p.Name() != tc.wantProvider {
				t.Errorf("provider: want %q, got %q", tc.wantProvider, p.Name())
			}
			if modelID != tc.wantModelID {
				t.Errorf("modelID: want %q, got %q", tc.wantModelID, modelID)
			}
		})
	}
}

func TestResolve_UnknownPrefix(t *testing.T) {
	_, _, err := llm.Resolve("unknown-model-xyz")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
	if !errors.Is(err, llm.ErrUnknownModel) {
		t.Errorf("want ErrUnknownModel, got %v", err)
	}
}
