package codex

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

func TestProviderName(t *testing.T) {
	if got := New().Name(); got != providerName {
		t.Fatalf("New().Name() = %q, want %q", got, providerName)
	}
}

func TestStream_UsesCodexEnvAndOpenAICompatibleEndpoint(t *testing.T) {
	var gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong Authorization header: %s", r.Header.Get("Authorization"))
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel = body.Model

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"pong"},"finish_reason":null}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	t.Setenv(apiKeyEnv, "test-key")
	t.Setenv(baseURLEnv, srv.URL)

	ch, err := New().Stream(context.Background(), llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var gotText string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		gotText += chunk.DeltaText
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotModel != "gpt-5.5" {
		t.Errorf("wire model = %q, want stripped model id gpt-5.5", gotModel)
	}
	if gotText != "pong" {
		t.Errorf("text = %q, want pong", gotText)
	}
}

func TestStream_MissingAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	_, err := New().Stream(context.Background(), llm.Request{Model: "gpt-5.5"})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), apiKeyEnv) {
		t.Errorf("error should mention %s, got: %v", apiKeyEnv, err)
	}
}
