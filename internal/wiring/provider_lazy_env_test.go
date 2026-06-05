package wiring

import (
	"context"
	"errors"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// TestBuildProvider_PostStartupKey_NotRejectedAsMissing is the
// regression test for the user-reported bug:
//
//	[error: agent: provider stream error: llm: missing API key:
//	 GOOGLE_API_KEY not set]
//
// happening immediately after /login google saved the key. The cause
// was that every provider singleton was registered in init() with the
// env var captured by value — so any key set later (via secrets.Add
// from /login) was invisible to the cached singleton's Stream call.
//
// This test runs in the normal test binary, where provider init()
// has already run with whatever env the developer's shell had. We
// then Setenv a fake key (mimicking secrets.Add) and confirm that:
//
//  1. wiring.BuildProvider succeeds (its pre-flight env check sees
//     the new key).
//  2. The returned provider's Stream call does NOT bail out with
//     ErrMissingAPIKey — i.e. the singleton's resolveKey() picked up
//     the post-init env. Any other error (network/HTTP, expected
//     because the fake key is unauthorized at the real endpoint) is
//     acceptable here; we only care that the missing-key gate fell.
//
// Pre-fix: this test fails with ErrMissingAPIKey because the singleton
// captured "" at init().
// Post-fix: this test passes — the singleton reads env per call.
func TestBuildProvider_PostStartupKey_NotRejectedAsMissing(t *testing.T) {
	cases := []struct {
		name, model, envVar string
	}{
		{"anthropic", "claude-3-5-sonnet-20241022", "ANTHROPIC_API_KEY"},
		{"google", "gemini-2.5-flash", "GOOGLE_API_KEY"},
		{"openai", "gpt-4o-mini", "OPENAI_API_KEY"},
		{"codex", "codex/gpt-5.5", "CODEX_API_KEY"},
		{"moonshot", "kimi-k2-instruct", "MOONSHOT_API_KEY"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Simulate /login secrets.Add — set the env AFTER provider
			// init() has already cached the singleton. Without the fix,
			// the singleton's apiKey field is "" forever.
			t.Setenv(c.envVar, "fake-key-set-post-init")

			prov, modelID, err := BuildProvider(c.model)
			if err != nil {
				t.Fatalf("BuildProvider(%q) should succeed once env is set: %v",
					c.model, err)
			}
			if modelID == "" {
				t.Fatal("BuildProvider returned empty modelID")
			}

			// Cancel the context BEFORE the HTTP roundtrip so we don't
			// actually hit the network. The missing-key check happens
			// before the request, so a cancelled context still exercises
			// it. Order of checks: resolveKey() → buildRequest() → http
			// → context cancel surfaces somewhere along the way.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, streamErr := prov.Stream(ctx, llm.Request{
				Model: modelID,
				Messages: []llm.Message{
					{Role: llm.RoleUser, Content: "ping"},
				},
			})
			if streamErr == nil {
				// Unexpected, but not a regression for this test.
				return
			}
			if errors.Is(streamErr, llm.ErrMissingAPIKey) {
				t.Errorf("regression: %s Stream rejected with ErrMissingAPIKey "+
					"despite env being set post-init: %v",
					c.name, streamErr)
			}
		})
	}
}
