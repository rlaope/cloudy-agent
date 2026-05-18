// Package moonshot implements an llm.Provider adapter for the Moonshot/Kimi API
// (kimi-* and moonshot-* model families).
//
// The Moonshot API is OpenAI-compatible; this package reuses the OpenAI SSE
// wire format with a different base URL and API key environment variable.
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
//	MOONSHOT_API_KEY – required
package moonshot

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

const defaultBaseURL = "https://api.moonshot.cn/v1"

// provider implements llm.Provider for Moonshot/Kimi.
type provider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func init() {
	llm.Register(New())
}

// New returns a Moonshot provider. The API key is intentionally NOT captured
// at construction time — registration happens in init() before /login can
// run, so a captured key would always be empty for fresh users. The key is
// read lazily from MOONSHOT_API_KEY on every Stream call.
func New() llm.Provider {
	base := os.Getenv("MOONSHOT_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	return &provider{
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "moonshot" }

// resolveKey returns the API key for this call. Test-injection field wins;
// production leaves it empty and falls through to the env var.
func (p *provider) resolveKey() string {
	if p.apiKey != "" {
		return p.apiKey
	}
	return os.Getenv("MOONSHOT_API_KEY")
}

// Stream implements llm.Provider using the Moonshot OpenAI-compatible streaming API.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	apiKey := p.resolveKey()
	if apiKey == "" {
		return nil, fmt.Errorf("%w: MOONSHOT_API_KEY not set", llm.ErrMissingAPIKey)
	}

	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("moonshot: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moonshot: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("moonshot: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("moonshot: API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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

type msRequest struct {
	Model       string      `json:"model"`
	Messages    []msMessage `json:"messages"`
	Tools       []msToolDef `json:"tools,omitempty"`
	Stream      bool        `json:"stream"`
	Temperature float64     `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
}

type msMessage struct {
	Role       string       `json:"role"`
	Content    interface{}  `json:"content"`
	ToolCalls  []msToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type msToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function msToolCallFunction `json:"function"`
}

type msToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type msToolDef struct {
	Type     string        `json:"type"`
	Function msFunctionDef `json:"function"`
}

type msFunctionDef struct {
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
	Content   string       `json:"content"`
	ToolCalls []msToolCall `json:"tool_calls"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func buildRequest(req llm.Request) ([]byte, error) {
	msgs := make([]msMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		mm := msMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if m.Content == "" {
			mm.Content = nil
		}
		for _, tc := range m.ToolCalls {
			mm.ToolCalls = append(mm.ToolCalls, msToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: msToolCallFunction{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		msgs = append(msgs, mm)
	}

	tools := make([]msToolDef, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, msToolDef{
			Type: "function",
			Function: msFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}

	return json.Marshal(msRequest{
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
				ch <- llm.Chunk{ToolCall: &cp}
			}
			ch <- llm.Chunk{Done: true}
			return
		}

		var event sseChunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- llm.Chunk{Err: fmt.Errorf("moonshot: parse SSE: %w", err)}
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
		ch <- llm.Chunk{Err: fmt.Errorf("moonshot: read stream: %w", err)}
		return
	}
	// Flush any accumulated tool calls if stream ended without [DONE].
	for _, tc := range toolAccum {
		cp := *tc
		ch <- llm.Chunk{ToolCall: &cp}
	}
	ch <- llm.Chunk{Done: true}
}
