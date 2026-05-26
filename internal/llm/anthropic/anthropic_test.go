package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"
)

func makeProvider(apiKey string, srv *httptest.Server) *provider {
	base := ""
	if srv != nil {
		base = srv.URL
	}
	// Override the package-level baseURL by building the provider directly.
	p := &provider{
		apiKey: apiKey,
		client: &http.Client{Transport: http.DefaultTransport},
	}
	_ = base // used below via monkey-patching via closure in test server
	return p
}

// testProvider builds a provider that POSTs to the given base URL.
func testProvider(base, apiKey string) *provider {
	return &provider{
		apiKey: apiKey,
		client: &http.Client{Transport: http.DefaultTransport},
	}
}

// streamingProvider injects a custom base for testing by returning a modified stream func.
// Since baseURL is a package-level const we instead spin the real provider and redirect
// via a round-tripper that rewrites the host.
func redirectProvider(apiKey, targetBase string) *provider {
	rt := &redirectTransport{base: strings.TrimRight(targetBase, "/")}
	return &provider{
		apiKey: apiKey,
		client: &http.Client{Transport: rt},
	}
}

type redirectTransport struct {
	base string
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = strings.TrimPrefix(r.base, "http://")
	return http.DefaultTransport.RoundTrip(req2)
}

func TestStream_TextChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") != anthropicVer {
			t.Errorf("missing anthropic-version header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: content_block_delta")
		fmt.Fprintln(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: content_block_delta")
		fmt.Fprintln(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: message_stop")
		fmt.Fprintln(w, `data: {"type":"message_stop"}`)
	}))
	defer srv.Close()

	p := redirectProvider("test-key", srv.URL)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var texts []string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if chunk.Done {
			break
		}
		if chunk.DeltaText != "" {
			texts = append(texts, chunk.DeltaText)
		}
	}

	got := strings.Join(texts, "")
	if got != "Hello world" {
		t.Errorf("want %q, got %q", "Hello world", got)
	}
}

func TestStream_ToolCallNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: content_block_start")
		fmt.Fprintln(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call1","name":"get_weather"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: content_block_delta")
		fmt.Fprintln(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: content_block_delta")
		fmt.Fprintln(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Seoul\"}"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: content_block_stop")
		fmt.Fprintln(w, `data: {"type":"content_block_stop","index":0}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: message_stop")
		fmt.Fprintln(w, `data: {"type":"message_stop"}`)
	}))
	defer srv.Close()

	p := redirectProvider("test-key", srv.URL)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "weather?"}},
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var toolChunks []*llm.ToolCall
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if chunk.ToolCall != nil {
			toolChunks = append(toolChunks, chunk.ToolCall)
		}
		if chunk.Done {
			break
		}
	}

	if len(toolChunks) == 0 {
		t.Fatal("expected at least one tool call chunk")
	}
	tc := toolChunks[0]
	if tc.ID != "call1" {
		t.Errorf("tool call ID: want %q, got %q", "call1", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("tool name: want %q, got %q", "get_weather", tc.Name)
	}
	if !strings.Contains(string(tc.Arguments), "Seoul") {
		t.Errorf("arguments missing Seoul: %s", tc.Arguments)
	}
}

func TestStream_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "") // defeat the lazy env-fallback in resolveKey
	p := redirectProvider("", "http://localhost")
	_, err := p.Stream(context.Background(), llm.Request{Model: "claude-3-5-sonnet-20241022"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention ANTHROPIC_API_KEY, got: %v", err)
	}
}

func TestStream_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`)
	}))
	defer srv.Close()

	p := redirectProvider("bad-key", srv.URL)
	_, err := p.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain 401, got: %v", err)
	}
}

// TestStream_ToolCallNoInput_NormalizedToEmptyObject covers a parameter-less
// tool call (e.g. k8s_list_nodes). The Anthropic stream sends content_block_
// start → content_block_stop with NO input_json_delta in between, leaving the
// args buffer empty. The previous behavior left tc.Arguments as `RawMessage(nil)`,
// which then caused buildRequest's `omitempty` on Input to drop the field on
// the next turn — and Anthropic returns HTTP 400 with
// `messages.<n>.content.<i>.tool_use.input: Field required`.
func TestStream_ToolCallNoInput_NormalizedToEmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: content_block_start")
		fmt.Fprintln(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call1","name":"k8s_list_nodes"}}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: content_block_stop")
		fmt.Fprintln(w, `data: {"type":"content_block_stop","index":0}`)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "event: message_stop")
		fmt.Fprintln(w, `data: {"type":"message_stop"}`)
	}))
	defer srv.Close()

	p := redirectProvider("test-key", srv.URL)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "list nodes"}},
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var toolChunks []*llm.ToolCall
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if chunk.ToolCall != nil {
			toolChunks = append(toolChunks, chunk.ToolCall)
		}
		if chunk.Done {
			break
		}
	}
	if len(toolChunks) != 1 {
		t.Fatalf("expected 1 tool chunk, got %d", len(toolChunks))
	}
	if string(toolChunks[0].Arguments) != "{}" {
		t.Errorf("Arguments = %q, want %q (empty input must be normalized to {} so the next turn does not 400)", string(toolChunks[0].Arguments), "{}")
	}
}

// TestBuildRequest_ToolUseEmptyInput verifies the outbound request shape when
// re-serializing an assistant message that includes a parameter-less or
// malformed tool call. The `input` field must always be present as a JSON
// object on the tool_use block, regardless of how Arguments got set upstream.
//
// The assertion walks the parsed body to the actual tool_use block instead of
// substring-matching `"input":{}` in the raw bytes, so a future request shape
// that happens to embed `"input_schema":{}` somewhere else cannot false-pass.
func TestBuildRequest_ToolUseEmptyInput(t *testing.T) {
	cases := []struct {
		name string
		args json.RawMessage
		// wantInput is the expected canonical Input on the tool_use block.
		// For non-object shapes (nil/empty/null/whitespace/partial), the
		// normalizer must collapse to {}. For an already-valid object, the
		// helper passes the original through unchanged.
		wantInput string
	}{
		{"nil_args", nil, "{}"},
		{"empty_args", json.RawMessage{}, "{}"},
		{"already_empty_obj", json.RawMessage(`{}`), "{}"},
		{"json_null", json.RawMessage(`null`), "{}"},
		{"whitespace_only", json.RawMessage(`   `), "{}"},
		{"partial_json", json.RawMessage(`{"name":`), "{}"},
		{"non_object_literal", json.RawMessage(`"hello"`), "{}"},
		{"populated_object", json.RawMessage(`{"namespace":"prod"}`), `{"namespace":"prod"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.Request{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []llm.Message{
					{Role: llm.RoleUser, Content: "go"},
					{
						Role:      llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{ID: "call1", Name: "k8s_list_nodes", Arguments: tc.args}},
					},
					{Role: llm.RoleTool, ToolCallID: "call1", Content: "ok"},
				},
			}
			body, err := buildRequest(req)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			// Parse to the actual tool_use content block. Content can be a
			// plain string (text-only messages) or an array of content
			// blocks, so we decode Content as RawMessage and try the array
			// shape per message — this is more robust than substring matching
			// `"input":{}` in the raw body, which could false-pass if a
			// tool's input_schema happened to encode to {}.
			var got struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode body: %v\nbody=%s", err, body)
			}
			var found bool
			for _, m := range got.Messages {
				if m.Role != "assistant" {
					continue
				}
				var blocks []antContentBlock
				if err := json.Unmarshal(m.Content, &blocks); err != nil {
					t.Fatalf("assistant content not a block array: %v\ncontent=%s", err, m.Content)
				}
				for _, b := range blocks {
					if b.Type != "tool_use" || b.ID != "call1" {
						continue
					}
					found = true
					if string(b.Input) != tc.wantInput {
						t.Errorf("tool_use.input = %q, want %q (body=%s)", string(b.Input), tc.wantInput, body)
					}
				}
			}
			if !found {
				t.Fatalf("did not find tool_use block with id=call1 in body:\n%s", body)
			}
		})
	}
}

// Note: the unit-level coverage that used to live here (TestNormalizeToolInput
// against the package-local shim) now lives in internal/llm/args_test.go's
// TestNormalizeArguments — the shim was removed in this PR so anthropic and
// every other provider share the same exported helper. The boundary tests in
// this file (TestBuildRequest_ToolUseEmptyInput) still exercise the helper
// end-to-end through the Anthropic wire format.
