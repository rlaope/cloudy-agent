package moonshot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"
)

func makeProvider(baseURL, apiKey string) *provider {
	return &provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Transport: http.DefaultTransport},
	}
}

func TestStream_TextChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong Authorization header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"안녕"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"하세요"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "moonshot-v1-8k",
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
	if got != "안녕하세요" {
		t.Errorf("want %q, got %q", "안녕하세요", got)
	}
}

func TestStream_ToolCallNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"id":"call1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":"\"kimi\"}"}}]},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "kimi-latest",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "search"}},
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
	if tc.Name != "search" {
		t.Errorf("tool name: want %q, got %q", "search", tc.Name)
	}
	if !strings.Contains(string(tc.Arguments), "kimi") {
		t.Errorf("arguments missing 'kimi': %s", tc.Arguments)
	}
}

func TestStream_MissingAPIKey(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "") // defeat the lazy env-fallback in resolveKey
	p := makeProvider("http://localhost", "")
	_, err := p.Stream(context.Background(), llm.Request{Model: "moonshot-v1-8k"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "MOONSHOT_API_KEY") {
		t.Errorf("error should mention MOONSHOT_API_KEY, got: %v", err)
	}
}

func TestStream_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, `{"error":"rate limit exceeded"}`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "test-key")
	_, err := p.Stream(context.Background(), llm.Request{
		Model:    "moonshot-v1-8k",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should contain 429, got: %v", err)
	}
}
