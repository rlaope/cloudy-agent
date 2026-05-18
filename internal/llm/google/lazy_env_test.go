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

// TestLazyEnvRead_PostConstructionSetenv simulates the exact production
// bug that motivated the resolveKey() change:
//
//  1. init() registers the singleton with empty GOOGLE_API_KEY (fresh
//     install, no shell export).
//  2. The operator runs /login google → cloudy calls secrets.Add which
//     os.Setenv's the key AFTER init() has already run.
//  3. The next user prompt routes to the same singleton.
//
// Before the fix, the singleton had p.apiKey = "" captured at New()
// and Stream rejected with ErrMissingAPIKey forever. After the fix,
// Stream reads via resolveKey() which falls back to os.Getenv on
// every call, so the post-construction Setenv takes effect.
func TestLazyEnvRead_PostConstructionSetenv(t *testing.T) {
	// Step 1: simulate init() seeing empty env.
	t.Setenv("GOOGLE_API_KEY", "")
	p := New().(*provider)
	if p.apiKey != "" {
		t.Fatalf("regression: New() must not capture GOOGLE_API_KEY at construction "+
			"time (the whole point of the fix); got %q", p.apiKey)
	}

	// Step 2: /login fires later → secrets.Add → os.Setenv.
	t.Setenv("GOOGLE_API_KEY", "key-from-login")

	// Step 3: redirect the singleton's HTTP client to a test server that
	// asserts the URL query carries the freshly-set key. Gemini sends the
	// API key as ?key=… (not a header).
	var seenKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	p.client = &http.Client{Transport: &redirectTransport{base: host}}

	ch, err := p.Stream(context.Background(), llm.Request{
		Model:    "gemini-1.5-pro",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream should succeed once env is set post-construction; got: %v", err)
	}
	for range ch {
		// drain so the goroutine exits cleanly
	}

	if seenKey != "key-from-login" {
		t.Errorf("server saw key=%q, want %q — lazy env-read is not wired through",
			seenKey, "key-from-login")
	}
}
