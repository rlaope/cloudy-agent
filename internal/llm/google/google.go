// Package google implements an llm.Provider adapter for the Google Gemini
// generateContent streaming API (gemini-* model family).
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
//	GOOGLE_API_KEY – required
package google

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

const baseURL = "https://generativelanguage.googleapis.com/v1beta/models"

// provider implements llm.Provider for Google Gemini.
type provider struct {
	apiKey string
	client *http.Client
}

func init() {
	llm.Register(New())
}

// New returns a Google provider reading credentials from environment variables.
func New() llm.Provider {
	return &provider{
		apiKey: os.Getenv("GOOGLE_API_KEY"),
		client: &http.Client{Transport: llmTransport},
	}
}

// Name implements llm.Provider.
func (p *provider) Name() string { return "google" }

// Stream implements llm.Provider using Gemini's streamGenerateContent endpoint.
func (p *provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("%w: GOOGLE_API_KEY not set", llm.ErrMissingAPIKey)
	}

	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("google: build request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:streamGenerateContent?key=%s&alt=sse",
		baseURL, req.Model, p.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("google: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("google: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("google: API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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

type gemRequest struct {
	Contents          []gemContent  `json:"contents"`
	Tools             []gemToolDef  `json:"tools,omitempty"`
	GenerationConfig  *gemGenConfig `json:"generationConfig,omitempty"`
	SystemInstruction *gemContent   `json:"systemInstruction,omitempty"`
}

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

type gemPart struct {
	Text             string               `json:"text,omitempty"`
	FunctionCall     *gemFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *gemFunctionResponse `json:"functionResponse,omitempty"`
}

type gemFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gemFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gemToolDef struct {
	FunctionDeclarations []gemFunctionDecl `json:"functionDeclarations"`
}

type gemFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type gemGenConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

// SSE response types
type gemResponse struct {
	Candidates    []gemCandidate `json:"candidates"`
	UsageMetadata *gemUsage      `json:"usageMetadata"`
}

type gemCandidate struct {
	Content gemContent `json:"content"`
}

type gemUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
}

func buildRequest(req llm.Request) ([]byte, error) {
	var systemInstr *gemContent
	var contents []gemContent

	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			systemInstr = &gemContent{
				Parts: []gemPart{{Text: m.Content}},
			}
			continue
		}

		var parts []gemPart
		switch m.Role {
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					parts = append(parts, gemPart{
						FunctionCall: &gemFunctionCall{
							Name: tc.Name,
							Args: tc.Arguments,
						},
					})
				}
			} else {
				parts = []gemPart{{Text: m.Content}}
			}
			contents = append(contents, gemContent{Role: "model", Parts: parts})
		case llm.RoleTool:
			resp, _ := json.Marshal(map[string]string{"output": m.Content})
			parts = []gemPart{{
				FunctionResponse: &gemFunctionResponse{
					Name:     m.ToolCallID,
					Response: resp,
				},
			}}
			contents = append(contents, gemContent{Role: "user", Parts: parts})
		default:
			contents = append(contents, gemContent{
				Role:  "user",
				Parts: []gemPart{{Text: m.Content}},
			})
		}
	}

	var tools []gemToolDef
	if len(req.Tools) > 0 {
		decls := make([]gemFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, gemFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			})
		}
		tools = []gemToolDef{{FunctionDeclarations: decls}}
	}

	var genCfg *gemGenConfig
	if req.Temperature != 0 || req.MaxTokens != 0 {
		genCfg = &gemGenConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		}
	}

	return json.Marshal(gemRequest{
		Contents:          contents,
		Tools:             tools,
		GenerationConfig:  genCfg,
		SystemInstruction: systemInstr,
	})
}

func parseSSE(ctx context.Context, r io.Reader, ch chan<- llm.Chunk) {
	scanner := bufio.NewScanner(r)
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

		var resp gemResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			ch <- llm.Chunk{Err: fmt.Errorf("google: parse SSE: %w", err)}
			return
		}

		var chunk llm.Chunk
		if resp.UsageMetadata != nil {
			chunk.Usage = &llm.Usage{
				InputTokens:  resp.UsageMetadata.PromptTokenCount,
				OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
			}
		}

		for _, cand := range resp.Candidates {
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					chunk.DeltaText += part.Text
				}
				if part.FunctionCall != nil {
					fc := part.FunctionCall
					cp := llm.ToolCall{
						ID:        fc.Name, // Gemini has no separate call ID; use name
						Name:      fc.Name,
						Arguments: fc.Args,
					}
					// emit tool calls immediately
					ch <- llm.Chunk{ToolCall: &cp}
				}
			}
		}

		if chunk.DeltaText != "" || chunk.Usage != nil {
			ch <- chunk
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.Chunk{Err: fmt.Errorf("google: read stream: %w", err)}
		return
	}
	ch <- llm.Chunk{Done: true}
}
