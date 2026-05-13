// Package anthropic implements an llm.Provider adapter for the Anthropic
// Messages API (claude-* model family).
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
//	ANTHROPIC_API_KEY – required
package anthropic

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

const (
	baseURL      = "https://api.anthropic.com"
	anthropicVer = "2023-06-01"
)

// provider implements llm.Provider for Anthropic.
type provider struct {
	apiKey string
	client *http.Client
}

func init() {
	llm.Register(New())
}

// New returns an Anthropic provider reading credentials from environment variables.
func New() llm.Provider {
	return &provider{
		apiKey: os.Getenv("ANTHROPIC_API_KEY"),
		client: &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "anthropic" }

// Stream implements llm.Provider using Anthropic's streaming Messages API.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("%w: ANTHROPIC_API_KEY not set", llm.ErrMissingAPIKey)
	}

	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVer)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic: API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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

type antRequest struct {
	Model       string       `json:"model"`
	System      string       `json:"system,omitempty"`
	Messages    []antMessage `json:"messages"`
	Tools       []antTool    `json:"tools,omitempty"`
	Stream      bool         `json:"stream"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature float64      `json:"temperature,omitempty"`
}

type antMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []antContentBlock
}

type antContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// SSE event types
type antEvent struct {
	Type  string    `json:"type"`
	Index int       `json:"index"`
	Delta *antDelta `json:"delta"`
	Usage *antUsage `json:"usage"`
}

type antDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// contentBlockStart carries the initial block definition.
type antContentBlockStart struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	ContentBlock *antInitBlock `json:"content_block"`
}

type antInitBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func buildRequest(req llm.Request) ([]byte, error) {
	var systemPrompt string
	var msgs []antMessage

	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			systemPrompt = m.Content
			continue
		}
		switch m.Role {
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var blocks []antContentBlock
				if m.Content != "" {
					blocks = append(blocks, antContentBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					blocks = append(blocks, antContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: tc.Arguments,
					})
				}
				msgs = append(msgs, antMessage{Role: "assistant", Content: blocks})
			} else {
				msgs = append(msgs, antMessage{Role: "assistant", Content: m.Content})
			}
		case llm.RoleTool:
			block := antContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			msgs = append(msgs, antMessage{Role: "user", Content: []antContentBlock{block}})
		default:
			msgs = append(msgs, antMessage{Role: string(m.Role), Content: m.Content})
		}
	}

	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = 4096
	}

	tools := make([]antTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, antTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}

	return json.Marshal(antRequest{
		Model:       req.Model,
		System:      systemPrompt,
		Messages:    msgs,
		Tools:       tools,
		Stream:      true,
		MaxTokens:   maxTok,
		Temperature: req.Temperature,
	})
}

func parseSSE(ctx context.Context, r io.Reader, ch chan<- llm.Chunk) {
	scanner := bufio.NewScanner(r)
	// track tool_use blocks by index
	toolBlocks := map[int]*llm.ToolCall{}
	var argsBuf = map[int][]byte{}

	var eventType string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- llm.Chunk{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "content_block_start":
			var ev antContentBlockStart
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				toolBlocks[ev.Index] = &llm.ToolCall{
					ID:   ev.ContentBlock.ID,
					Name: ev.ContentBlock.Name,
				}
				argsBuf[ev.Index] = nil
			}

		case "content_block_delta":
			var ev antEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- llm.Chunk{DeltaText: ev.Delta.Text}
				}
			case "input_json_delta":
				argsBuf[ev.Index] = append(argsBuf[ev.Index], ev.Delta.PartialJSON...)
			}

		case "content_block_stop":
			var ev struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if tc, ok := toolBlocks[ev.Index]; ok {
				tc.Arguments = json.RawMessage(argsBuf[ev.Index])
				cp := *tc
				ch <- llm.Chunk{ToolCall: &cp}
				delete(toolBlocks, ev.Index)
				delete(argsBuf, ev.Index)
			}

		case "message_delta":
			var ev antEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.Usage != nil {
				ch <- llm.Chunk{Usage: &llm.Usage{
					InputTokens:  ev.Usage.InputTokens,
					OutputTokens: ev.Usage.OutputTokens,
				}}
			}

		case "message_stop":
			ch <- llm.Chunk{Done: true}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.Chunk{Err: fmt.Errorf("anthropic: read stream: %w", err)}
		return
	}
	ch <- llm.Chunk{Done: true}
}
