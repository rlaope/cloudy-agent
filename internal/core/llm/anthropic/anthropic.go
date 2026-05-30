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

	"github.com/rlaope/cloudy/internal/core/llm"
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

// New returns an Anthropic provider. The API key is intentionally NOT
// captured at construction time — registration happens in init() before
// /login can run, so a captured key would always be empty for fresh
// users. The key is read lazily from ANTHROPIC_API_KEY on every Stream
// call so a post-startup secrets.Add takes effect on the next turn.
func New() llm.Provider {
	return &provider{
		client: &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "anthropic" }

// resolveKey returns the API key for this call. Test-injection field wins;
// production leaves it empty and falls through to the env var.
func (p *provider) resolveKey() string {
	if p.apiKey != "" {
		return p.apiKey
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// Stream implements llm.Provider using Anthropic's streaming Messages API.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	apiKey := p.resolveKey()
	if apiKey == "" {
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
	httpReq.Header.Set("x-api-key", apiKey)
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
	Model       string           `json:"model"`
	System      []antSystemBlock `json:"system,omitempty"`
	Messages    []antMessage     `json:"messages"`
	Tools       []antTool        `json:"tools,omitempty"`
	Stream      bool             `json:"stream"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature,omitempty"`
}

// antSystemBlock is one text block of the system prompt. The array form
// (vs. a bare string) is what lets us attach cache_control to the prompt
// so Anthropic caches the stable prefix server-side across turns.
type antSystemBlock struct {
	Type         string       `json:"type"` // "text"
	Text         string       `json:"text"`
	CacheControl *antCacheCtl `json:"cache_control,omitempty"`
}

// antCacheCtl marks a prompt-cache breakpoint. Everything up to and
// including a block carrying it is cached; type is always "ephemeral".
type antCacheCtl struct {
	Type string `json:"type"` // "ephemeral"
}

// ephemeralCache returns the single cache-breakpoint marker reused for the
// system prompt and the tool array.
func ephemeralCache() *antCacheCtl { return &antCacheCtl{Type: "ephemeral"} }

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
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *antCacheCtl    `json:"cache_control,omitempty"`
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
					// Anthropic requires tool_use.input to be present and to be
					// a JSON object. Our antContentBlock.Input is
					// `json:"input,omitempty"`, which would drop the field when
					// Arguments is nil/empty — exactly the shape models emit
					// for parameter-less tools like k8s_list_nodes; the API
					// responds with `messages.<n>.content.<i>.tool_use.input:
					// Field required` (HTTP 400). Normalize to `{}` in three
					// cases the bare `len==0` check would have missed:
					//   - nil / empty slice (the original bug)
					//   - JSON `null` (a 4-byte payload that is non-empty but
					//     fails Anthropic's object-shape validation)
					//   - any other non-object JSON or partial garbage that
					//     could land in args via a stream truncation or a
					//     replay of corrupted history
					input := llm.NormalizeArguments(tc.Arguments)
					blocks = append(blocks, antContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
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

	// Prompt caching: mark the stable prefix so Anthropic caches it
	// server-side across turns. The system prompt and the tool array are
	// the per-turn fixed portion; the conversation tail changes every turn
	// (and /compact rewrites it), so we never cache message blocks. Two
	// breakpoints, well within Anthropic's limit of four.
	var system []antSystemBlock
	if systemPrompt != "" {
		system = []antSystemBlock{{Type: "text", Text: systemPrompt, CacheControl: ephemeralCache()}}
	}
	if len(tools) > 0 {
		// A breakpoint on the last tool caches everything up to and
		// including it — i.e. the whole tool array.
		tools[len(tools)-1].CacheControl = ephemeralCache()
	}

	return json.Marshal(antRequest{
		Model:       req.Model,
		System:      system,
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
				// Normalize parameter-less / null / truncated args to `{}` so
				// the rest of cloudy never has to special-case the empty form
				// and the next-turn buildRequest cannot ship a malformed
				// tool_use block. See llm.NormalizeArguments for the full
				// rule (this seam is what produced `tool_use.input: Field
				// required` 400s pre-v0.5).
				tc.Arguments = llm.NormalizeArguments(argsBuf[ev.Index])
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
