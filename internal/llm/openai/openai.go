// Package openai implements an llm.Provider adapter for the OpenAI Chat
// Completions API (including gpt-* and o1-* model families).
//
// # Read-only transport note
//
// cloudy's ReadOnlyRoundTripper (internal/transport) enforces GET/HEAD/OPTIONS
// only and is designed for infrastructure-facing calls (Kubernetes, Prometheus).
// LLM API calls are user-equivalent egress and intentionally bypass that guard.
// This package uses its own unexported llmTransport (a plain http.DefaultTransport
// alias) so there is zero risk of the two transports being confused.
//
// Configuration (environment variables):
//
//	OPENAI_API_KEY      – required
//	OPENAI_BASE_URL     – optional; defaults to https://api.openai.com
package openai

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
// It is deliberately separate from cloudy's read-only infrastructure transport.
var llmTransport http.RoundTripper = http.DefaultTransport

const defaultBaseURL = "https://api.openai.com"

// provider implements llm.Provider for OpenAI.
type provider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func init() {
	llm.Register(New())
}

// New returns an OpenAI provider reading credentials from environment variables.
func New() llm.Provider {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	return &provider{
		baseURL: strings.TrimRight(base, "/"),
		apiKey:  os.Getenv("OPENAI_API_KEY"),
		client:  &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "openai" }

// Stream implements llm.Provider using OpenAI's SSE streaming completions.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("%w: OPENAI_API_KEY not set", llm.ErrMissingAPIKey)
	}

	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai: API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	ch := make(chan llm.Chunk, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		parseSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// --- wire types ---

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Tools       []oaiTool    `json:"tools,omitempty"`
	Stream      bool         `json:"stream"`
	Temperature float64      `json:"temperature,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content"` // string or null
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiToolDef struct {
	Type     string             `json:"type"`
	Function oaiToolFunctionDef `json:"function"`
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
	Content   string        `json:"content"`
	ToolCalls []oaiToolCall `json:"tool_calls"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func buildRequest(req llm.Request) ([]byte, error) {
	msgs := make([]oaiMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := oaiMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if m.Content == "" {
			om.Content = nil
		}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, oaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: oaiToolFunction{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		msgs = append(msgs, om)
	}

	tools := make([]oaiToolDef, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, oaiToolDef{
			Type: "function",
			Function: oaiToolFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}

	payload := oaiRequest{
		Model:       req.Model,
		Messages:    msgs,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if len(tools) > 0 {
		payload.Tools = nil // use raw field below
	}

	// Build final payload with tools field only when non-empty.
	type fullReq struct {
		Model       string       `json:"model"`
		Messages    []oaiMessage `json:"messages"`
		Tools       []oaiToolDef `json:"tools,omitempty"`
		Stream      bool         `json:"stream"`
		Temperature float64      `json:"temperature,omitempty"`
		MaxTokens   int          `json:"max_tokens,omitempty"`
	}
	return json.Marshal(fullReq{
		Model:       payload.Model,
		Messages:    payload.Messages,
		Tools:       tools,
		Stream:      payload.Stream,
		Temperature: payload.Temperature,
		MaxTokens:   payload.MaxTokens,
	})
}

func parseSSE(ctx context.Context, r io.Reader, ch chan<- llm.Chunk) {
	scanner := bufio.NewScanner(r)
	// accumulate tool call deltas: index → ToolCall
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
				ch <- llm.Chunk{ToolCall: &cp}
			}
			ch <- llm.Chunk{Done: true}
			return
		}

		var event sseChunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- llm.Chunk{Err: fmt.Errorf("openai: parse SSE: %w", err)}
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
			for _, tc := range choice.Delta.ToolCalls {
				// tc.Index is inlined as the position in the slice
				// OpenAI uses an "index" field; we key by ID prefix accumulation.
				idx := 0
				if len(choice.Delta.ToolCalls) > 1 {
					// fallback: use position
					for i, t := range choice.Delta.ToolCalls {
						if t.ID != "" {
							idx = i
						}
					}
				}
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
		ch <- llm.Chunk{Err: fmt.Errorf("openai: read stream: %w", err)}
		return
	}
	// Flush any accumulated tool calls if stream ended without [DONE].
	for _, tc := range toolAccum {
		cp := *tc
		ch <- llm.Chunk{ToolCall: &cp}
	}
	ch <- llm.Chunk{Done: true}
}
