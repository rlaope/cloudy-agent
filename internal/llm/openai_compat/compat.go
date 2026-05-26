// Package openai_compat implements an llm.Provider adapter for OpenAI-compatible
// APIs served by local or third-party inference engines such as Ollama, vLLM,
// LM Studio, and OpenRouter.
//
// Model names are prefixed with "local/" in the registry routing table; the
// prefix is stripped before being sent to the upstream API so that model
// identifiers like "local/llama3" reach the server as "llama3".
//
// # Read-only transport note
//
// cloudy's ReadOnlyRoundTripper (internal/transport) is reserved for
// infrastructure-facing calls (Kubernetes, Prometheus). LLM API calls are
// user-equivalent egress and intentionally bypass that guard. This package
// uses a plain http.Client backed by http.DefaultTransport.
//
// Configuration (environment variables):
//
//	CLOUDY_OPENAI_COMPAT_BASE_URL – required; e.g. http://localhost:11434/v1
//	CLOUDY_OPENAI_COMPAT_API_KEY  – optional; sent as Bearer token when set
package openai_compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"
)

// llmTransport is the HTTP transport used exclusively for LLM API egress.
var llmTransport http.RoundTripper = http.DefaultTransport

// provider implements llm.Provider for OpenAI-compatible local/remote APIs.
type provider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func init() {
	llm.Register(New())
}

// New returns an openai_compat provider reading configuration from environment variables.
func New() llm.Provider {
	base := os.Getenv("CLOUDY_OPENAI_COMPAT_BASE_URL")
	return &provider{
		baseURL: strings.TrimRight(base, "/"),
		apiKey:  os.Getenv("CLOUDY_OPENAI_COMPAT_API_KEY"),
		client:  &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "openai_compat" }

// Stream implements llm.Provider using the OpenAI-compatible SSE streaming endpoint.
// The model prefix "local/" has already been stripped by llm.Resolve before
// req.Model reaches this method.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if p.baseURL == "" {
		return nil, fmt.Errorf("openai_compat: CLOUDY_OPENAI_COMPAT_BASE_URL not set")
	}

	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai_compat: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai_compat: API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	ch := make(chan llm.Chunk, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		parseSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// --- wire types (OpenAI-compatible) ---

type compatRequest struct {
	Model       string          `json:"model"`
	Messages    []compatMessage `json:"messages"`
	Tools       []compatToolDef `json:"tools,omitempty"`
	Stream      bool            `json:"stream"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type compatMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content"`
	ToolCalls  []compatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type compatToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function compatToolCallFunction `json:"function"`
}

type compatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type compatToolDef struct {
	Type     string            `json:"type"`
	Function compatFunctionDef `json:"function"`
}

type compatFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// SSE event types
type sseChunk struct {
	Choices []sseChoice `json:"choices"`
	Usage   *sseUsage   `json:"usage"`
}

type sseChoice struct {
	Delta        sseDelta `json:"delta"`
	FinishReason string   `json:"finish_reason"`
}

type sseDelta struct {
	Content   string           `json:"content"`
	ToolCalls []compatToolCall `json:"tool_calls"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func buildRequest(req llm.Request) ([]byte, error) {
	msgs := make([]compatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		cm := compatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if m.Content == "" {
			cm.Content = nil
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, compatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: compatToolCallFunction{
					Name: tc.Name,
					// See llm.NormalizeArguments — local engines (vLLM with
					// guided-json, llama.cpp tool-use mode) can reject the
					// empty-string Arguments shape that nil/empty would
					// otherwise produce.
					Arguments: string(llm.NormalizeArguments(tc.Arguments)),
				},
			})
		}
		msgs = append(msgs, cm)
	}

	tools := make([]compatToolDef, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, compatToolDef{
			Type: "function",
			Function: compatFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}

	return json.Marshal(compatRequest{
		Model:       req.Model,
		Messages:    msgs,
		Tools:       tools,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})
}

func parseSSE(ctx context.Context, r io.Reader, ch chan<- llm.Chunk) {
	scanner := bufio.NewScanner(r)
	toolAccum := map[int]*llm.ToolCall{}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- llm.Chunk{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			for _, tc := range toolAccum {
				cp := *tc
				cp.Arguments = llm.NormalizeArguments(cp.Arguments)
				ch <- llm.Chunk{ToolCall: &cp}
			}
			ch <- llm.Chunk{Done: true}
			return
		}

		var event sseChunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- llm.Chunk{Err: fmt.Errorf("openai_compat: parse SSE: %w", err)}
			return
		}

		var chunk llm.Chunk
		if event.Usage != nil {
			chunk.Usage = &llm.Usage{
				InputTokens:  event.Usage.PromptTokens,
				OutputTokens: event.Usage.CompletionTokens,
			}
		}

		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				chunk.DeltaText += choice.Delta.Content
			}
			for idx, tc := range choice.Delta.ToolCalls {
				acc, ok := toolAccum[idx]
				if !ok {
					acc = &llm.ToolCall{ID: tc.ID, Name: tc.Function.Name}
					toolAccum[idx] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				acc.Arguments = append(acc.Arguments, []byte(tc.Function.Arguments)...)
			}
		}

		if chunk.DeltaText != "" || chunk.Usage != nil {
			ch <- chunk
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.Chunk{Err: fmt.Errorf("openai_compat: read stream: %w", err)}
		return
	}
	// Flush any accumulated tool calls if stream ended without [DONE].
	for _, tc := range toolAccum {
		cp := *tc
		cp.Arguments = llm.NormalizeArguments(cp.Arguments)
		ch <- llm.Chunk{ToolCall: &cp}
	}
	ch <- llm.Chunk{Done: true}
}
