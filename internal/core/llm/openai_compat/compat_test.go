package openai_compat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
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
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":" there"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "llama3",
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
	if got != "Hi there" {
		t.Errorf("want %q, got %q", "Hi there", got)
	}
}

func TestStream_ToolCallNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"id":"tc1","type":"function","function":{"name":"calc","arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "")
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "llama3",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "calc"}},
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
	if tc.Name != "calc" {
		t.Errorf("tool name: want %q, got %q", "calc", tc.Name)
	}
}

func TestStream_MissingBaseURL(t *testing.T) {
	t.Setenv("CLOUDY_OPENAI_COMPAT_BASE_URL", "")
	p := makeProvider("", "")
	_, err := p.Stream(context.Background(), llm.Request{Model: "llama3"})
	if err == nil {
		t.Fatal("expected error for missing base URL")
	}
	if !strings.Contains(err.Error(), "CLOUDY_OPENAI_COMPAT_BASE_URL") {
		t.Errorf("error should mention CLOUDY_OPENAI_COMPAT_BASE_URL, got: %v", err)
	}
}

func TestLazyEnvRead_PostConstructionSetenv(t *testing.T) {
	t.Setenv("CLOUDY_OPENAI_COMPAT_BASE_URL", "")
	p := New().(*provider)
	if p.baseURL != "" {
		t.Fatalf("test setup expected empty baseURL, got %q", p.baseURL)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()
	t.Setenv("CLOUDY_OPENAI_COMPAT_BASE_URL", srv.URL)

	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "llama3",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream should read base URL from env at call time: %v", err)
	}
	for range ch {
	}
}

func TestStream_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()

	p := makeProvider(srv.URL, "")
	_, err := p.Stream(context.Background(), llm.Request{
		Model:    "unknown-model",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should contain 400, got: %v", err)
	}
}
