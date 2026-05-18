package google

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"
)

// redirectProvider builds a provider that rewrites requests to the test server.
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
		if !strings.Contains(r.URL.RawQuery, "key=test-key") {
			t.Errorf("missing API key in query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`)
		fmt.Fprintln(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`)
	}))
	defer srv.Close()

	p := redirectProvider("test-key", srv.URL)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "gemini-1.5-pro",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var texts []string
	var gotUsage bool
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
		if chunk.Usage != nil {
			gotUsage = true
		}
	}

	got := strings.Join(texts, "")
	if got != "Hello world" {
		t.Errorf("want %q, got %q", "Hello world", got)
	}
	if !gotUsage {
		t.Error("expected usage chunk")
	}
}

func TestStream_ToolCallNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"Seoul"}}}]}}]}`)
	}))
	defer srv.Close()

	p := redirectProvider("test-key", srv.URL)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "gemini-1.5-pro",
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
	if !strings.Contains(string(tc.Arguments), "Seoul") {
		t.Errorf("arguments missing Seoul: %s", tc.Arguments)
	}
}

func TestStream_MissingAPIKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "") // defeat the lazy env-fallback in resolveKey
	p := redirectProvider("", "http://localhost")
	_, err := p.Stream(context.Background(), llm.Request{Model: "gemini-1.5-pro"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "GOOGLE_API_KEY") {
		t.Errorf("error should mention GOOGLE_API_KEY, got: %v", err)
	}
}

func TestStream_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintln(w, `{"error":{"code":403,"message":"API key not valid"}}`)
	}))
	defer srv.Close()

	p := redirectProvider("bad-key", srv.URL)
	_, err := p.Stream(context.Background(), llm.Request{
		Model:    "gemini-1.5-pro",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should contain 403, got: %v", err)
	}
}
