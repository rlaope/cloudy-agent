package anthropic

import (
	"context"
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
