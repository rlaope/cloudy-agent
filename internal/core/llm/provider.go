// Package llm provides a unified abstraction over multiple LLM providers.
//
// Usage:
//
//	provider, modelID, err := llm.Resolve("claude-3-5-sonnet-20241022")
//	if err != nil { ... }
//	ch, err := provider.Stream(ctx, llm.Request{Model: modelID, Messages: msgs})
//	for chunk := range ch { ... }
//
// Provider adapters register themselves via init() in their respective
// sub-packages. Import a sub-package for its side-effects to make it
// available to Resolve:
//
//	import _ "github.com/rlaope/cloudy/internal/core/llm/anthropic"
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/registry"
)

// Role represents the speaker role in a conversation turn.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall represents a single function/tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message is a single turn in a conversation.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// Tool describes a callable function the model may invoke.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Request is the provider-agnostic input to a streaming completion.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	Stream      bool
	Temperature float64
	MaxTokens   int
}

// Usage reports token consumption for a request.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Chunk is a single item emitted by a streaming completion.
type Chunk struct {
	DeltaText string
	ToolCall  *ToolCall
	Usage     *Usage
	Done      bool
	Err       error
}

// Provider is the interface every LLM adapter must satisfy.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// ErrUnknownModel is returned by Resolve when no provider matches the model prefix.
var ErrUnknownModel = errors.New("llm: unknown model prefix")

// ErrMissingAPIKey is returned when a required environment variable is absent.
var ErrMissingAPIKey = errors.New("llm: missing API key")

// providers is the global self-registration store, backed by the shared
// generic registry.Map.
var providers = registry.New[Provider](func(p Provider) string { return p.Name() })

// Register adds a provider to the global registry. Intended to be called
// from adapter init() functions; panics on duplicate names.
func Register(p Provider) { providers.MustRegister(p) }

// prefixMap maps model-string prefixes to provider names.
var prefixMap = []struct {
	prefix   string
	provider string
}{
	{"codex/", "codex"},
	{"gpt-", "openai"},
	{"o1-", "openai"},
	{"o3", "openai"},
	{"o4", "openai"},
	{"claude-", "anthropic"},
	{"gemini-", "google"},
	{"kimi-", "moonshot"},
	{"moonshot-", "moonshot"},
	{"local/", "openai_compat"},
}

// Resolve picks the correct Provider for the given model string, returning
// (provider, modelID, error). The returned modelID has any routing prefix
// ("codex/" or "local/") stripped.
func Resolve(model string) (Provider, string, error) {
	for _, entry := range prefixMap {
		if strings.HasPrefix(model, entry.prefix) {
			p, ok := providers.Get(entry.provider)
			if !ok {
				return nil, "", fmt.Errorf("llm: provider %q not registered (did you import its package?)", entry.provider)
			}
			modelID := model
			if entry.prefix == "codex/" || entry.prefix == "local/" {
				modelID = strings.TrimPrefix(model, entry.prefix)
			}
			return p, modelID, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %q", ErrUnknownModel, model)
}
