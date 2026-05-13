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
//	import _ "github.com/rlaope/cloudy/internal/llm/anthropic"
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Role represents the speaker role in a conversation turn.
type Role string

const (
	// RoleSystem is the system/instruction role.
	RoleSystem Role = "system"
	// RoleUser is the human turn role.
	RoleUser Role = "user"
	// RoleAssistant is the model turn role.
	RoleAssistant Role = "assistant"
	// RoleTool is the tool-result role.
	RoleTool Role = "tool"
)

// ToolCall represents a single function/tool invocation requested by the model.
type ToolCall struct {
	// ID is the provider-assigned call identifier (used to correlate results).
	ID string
	// Name is the tool/function name.
	Name string
	// Arguments is the JSON-encoded argument object.
	Arguments json.RawMessage
}

// Message is a single turn in a conversation.
type Message struct {
	// Role identifies who produced this message.
	Role Role
	// Content is the textual body of the message.
	Content string
	// ToolCalls holds any tool invocations requested in this turn (assistant role).
	ToolCalls []ToolCall
	// ToolCallID is the call ID this message is responding to (tool role).
	ToolCallID string
}

// Tool describes a callable function the model may invoke.
type Tool struct {
	// Name is the function identifier sent to the model.
	Name string
	// Description explains what the tool does (shown to the model).
	Description string
	// Schema is the JSON Schema for the tool's parameters.
	Schema json.RawMessage
}

// Request is the provider-agnostic input to a streaming completion.
type Request struct {
	// Model is the fully-qualified model identifier (e.g. "claude-3-5-sonnet-20241022").
	Model string
	// Messages is the ordered conversation history.
	Messages []Message
	// Tools is the optional list of callable tools.
	Tools []Tool
	// Stream enables server-sent-event streaming (always true in practice; kept for clarity).
	Stream bool
	// Temperature controls output randomness (0–2 for OpenAI; 0–1 for Anthropic).
	Temperature float64
	// MaxTokens caps the completion length. 0 means provider default.
	MaxTokens int
}

// Usage reports token consumption for a request.
type Usage struct {
	// InputTokens is the number of prompt tokens consumed.
	InputTokens int
	// OutputTokens is the number of completion tokens generated.
	OutputTokens int
	// CostUSD is the estimated cost in US dollars (best-effort; may be zero).
	CostUSD float64
}

// Chunk is a single item emitted by a streaming completion.
type Chunk struct {
	// DeltaText is the incremental text fragment for this chunk.
	DeltaText string
	// ToolCall carries a tool invocation delta, if any.
	ToolCall *ToolCall
	// Usage is populated on the final chunk when the provider reports token counts.
	Usage *Usage
	// Done signals that the stream has ended normally.
	Done bool
	// Err carries any error that terminated the stream abnormally.
	Err error
}

// Provider is the interface every LLM adapter must satisfy.
type Provider interface {
	// Name returns the short identifier for this provider (e.g. "openai").
	Name() string
	// Stream starts a streaming completion and returns a channel of Chunks.
	// The channel is closed after a Chunk with Done==true or Err!=nil is sent.
	// Callers must drain or cancel via ctx to avoid goroutine leaks.
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// ErrUnknownModel is returned by Resolve when no provider matches the model prefix.
var ErrUnknownModel = errors.New("llm: unknown model prefix")

// ErrMissingAPIKey is returned when a required environment variable is absent.
var ErrMissingAPIKey = errors.New("llm: missing API key")

// registry holds all registered providers keyed by short name.
var registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// Register adds a provider to the global registry.
// It panics if a provider with the same name is registered twice.
// Intended to be called from adapter init() functions.
func Register(p Provider) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.providers == nil {
		registry.providers = make(map[string]Provider)
	}
	if _, exists := registry.providers[p.Name()]; exists {
		panic(fmt.Sprintf("llm: provider %q already registered", p.Name()))
	}
	registry.providers[p.Name()] = p
}

// prefixMap maps model-string prefixes to provider names.
var prefixMap = []struct {
	prefix   string
	provider string
}{
	{"gpt-", "openai"},
	{"o1-", "openai"},
	{"claude-", "anthropic"},
	{"gemini-", "google"},
	{"kimi-", "moonshot"},
	{"moonshot-", "moonshot"},
	{"local/", "openai_compat"},
}

// Resolve picks the correct Provider for the given model string.
// It returns the provider, the model ID to pass to the upstream API
// (with any routing prefix stripped), and an error.
//
// Prefix routing rules:
//
//	"gpt-*" / "o1-*"           → openai
//	"claude-*"                 → anthropic
//	"gemini-*"                 → google
//	"kimi-*" / "moonshot-*"    → moonshot
//	"local/*"                  → openai_compat (strips "local/" prefix)
func Resolve(model string) (Provider, string, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	for _, entry := range prefixMap {
		if strings.HasPrefix(model, entry.prefix) {
			p, ok := registry.providers[entry.provider]
			if !ok {
				return nil, "", fmt.Errorf("llm: provider %q not registered (did you import its package?)", entry.provider)
			}
			modelID := model
			if entry.prefix == "local/" {
				modelID = strings.TrimPrefix(model, "local/")
			}
			return p, modelID, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %q", ErrUnknownModel, model)
}
