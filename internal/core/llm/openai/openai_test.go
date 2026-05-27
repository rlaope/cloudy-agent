package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// makeProvider returns a provider pointed at the given test server URL.
func makeProvider(baseURL, apiKey string) *provider {
	return &provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Transport: http.DefaultTransport},
	}
}

func TestStream_TextChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong Authorization header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "gpt-4o",
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
	args := `{"location":"Seoul"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// First chunk: start tool call
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n")
		// Second chunk: arguments fragment
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"\",\"type\":\"function\",\"function\":{\"name\":\"\",\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", jsonStr(args))
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "gpt-4o",
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
	if tc.Name != "get_weather" {
		t.Errorf("tool name: want %q, got %q", "get_weather", tc.Name)
	}
	// Arguments should contain the location JSON fragment
	if !strings.Contains(string(tc.Arguments), "Seoul") {
		t.Errorf("tool arguments missing expected content, got: %s", tc.Arguments)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestStream_MissingAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "") // defeat the lazy env-fallback in resolveKey
	p := makeProvider("http://localhost", "")
	_, err := p.Stream(context.Background(), llm.Request{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error should mention OPENAI_API_KEY, got: %v", err)
	}
}

func TestStream_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "bad-key")
	_, err := p.Stream(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status code 401, got: %v", err)
	}
}
