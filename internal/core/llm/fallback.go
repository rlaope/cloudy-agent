package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// FallbackProvider is a Provider that tries a primary first, then walks an
// ordered list of secondary candidates on failure. Each candidate is gated by
// its own circuit breaker so a known-bad endpoint is skipped for the remainder
// of the cooldown window instead of being retried on every request.
//
// FallbackProvider exists because external LLM APIs go down — and when they
// do, an SRE diagnostic session that cannot continue is most of the cost.
// Wrapping the primary with a local model (Ollama / vLLM via openai_compat)
// keeps the agent loop running through transient outages without changing
// every caller's retry code.
type FallbackProvider struct {
	chain    []Provider
	modelIDs []string // per-chain-entry model identifier (after prefix stripping)
	breakers []*breaker
}

// FallbackOptions configures a FallbackProvider.
type FallbackOptions struct {
	// Threshold is the number of failures within Window that opens the breaker.
	// Zero falls back to 3.
	Threshold int
	// Window is the failure-counting window. Zero falls back to 60s.
	Window time.Duration
}

// NewFallback wraps chain[0] with chain[1:] as ordered fallbacks. modelIDs
// must match chain length; each chain element will receive its own model ID
// when Stream is invoked. Empty chains panic — that is a programmer error.
func NewFallback(chain []Provider, modelIDs []string, opts FallbackOptions) *FallbackProvider {
	if len(chain) == 0 {
		panic("llm: NewFallback requires at least one Provider")
	}
	if len(chain) != len(modelIDs) {
		panic("llm: NewFallback chain and modelIDs length mismatch")
	}
	if opts.Threshold <= 0 {
		opts.Threshold = 3
	}
	if opts.Window <= 0 {
		opts.Window = 60 * time.Second
	}
	breakers := make([]*breaker, len(chain))
	for i := range chain {
		breakers[i] = newBreaker(opts.Threshold, opts.Window)
	}
	return &FallbackProvider{chain: chain, modelIDs: modelIDs, breakers: breakers}
}

// Name returns the synthetic name "fallback(<primary>+N)" so logs identify
// the wrapper without losing the primary identity.
func (p *FallbackProvider) Name() string {
	if len(p.chain) == 1 {
		return p.chain[0].Name()
	}
	return fmt.Sprintf("fallback(%s+%d)", p.chain[0].Name(), len(p.chain)-1)
}

// Stream tries each candidate in order, skipping ones whose breaker is open.
// The first successful Stream wins and its channel is forwarded to the caller
// with mid-stream errors recorded against that candidate's breaker.
func (p *FallbackProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	var firstErr error
	for i, candidate := range p.chain {
		if ctx.Err() != nil {
			// User cancelled — not a provider problem. Return immediately
			// without touching breakers.
			return nil, ctx.Err()
		}
		if p.breakers[i].IsOpen() {
			continue
		}
		// Each candidate gets its own model ID — primary's "claude-…" and
		// fallback's "llama3" share no namespace.
		candReq := req
		candReq.Model = p.modelIDs[i]
		ch, err := candidate.Stream(ctx, candReq)
		if err != nil {
			// ctx cancellation surfaced as ctx.Err on err — don't blame provider.
			if ctx.Err() == nil {
				p.breakers[i].recordFailure()
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", candidate.Name(), err)
			}
			continue
		}
		return p.wrapChunks(ch, p.breakers[i]), nil
	}
	if firstErr != nil {
		return nil, fmt.Errorf("llm: all %d providers failed; first error: %w", len(p.chain), firstErr)
	}
	return nil, errors.New("llm: no provider available (all circuit breakers open)")
}

// wrapChunks forwards chunks while recording any mid-stream Err against the
// breaker so transient mid-conversation failures still count toward opening.
func (p *FallbackProvider) wrapChunks(in <-chan Chunk, b *breaker) <-chan Chunk {
	out := make(chan Chunk, cap(in)+1)
	go func() {
		defer close(out)
		for c := range in {
			if c.Err != nil {
				b.recordFailure()
			}
			out <- c
		}
	}()
	return out
}

// CompatFallbackBaseURLEnv is the env var that, when set, opts the user in to
// an automatic openai_compat fallback layered behind whatever primary
// Resolve picked.
const CompatFallbackBaseURLEnv = "CLOUDY_OPENAI_COMPAT_BASE_URL"

// CompatFallbackModelEnv overrides the default local model identifier sent
// to the openai_compat fallback. When unset, "llama3" is used.
const CompatFallbackModelEnv = "CLOUDY_OPENAI_COMPAT_FALLBACK_MODEL"

// WrapWithCompatFallback returns primary unchanged when no
// CLOUDY_OPENAI_COMPAT_BASE_URL is set or when primary already is the
// openai_compat provider. Otherwise it returns a FallbackProvider chaining
// primary → openai_compat so a transient API outage falls through to a
// locally-served model rather than aborting the agent loop.
//
// Wiring code calls this after the per-provider API-key check on primary, so
// a missing primary key still fails fast — the fallback layer only kicks in
// for runtime / network failures, not configuration errors.
func WrapWithCompatFallback(primary Provider, primaryModelID string) Provider {
	if strings.TrimSpace(os.Getenv(CompatFallbackBaseURLEnv)) == "" {
		return primary
	}
	if primary.Name() == "openai_compat" {
		return primary
	}
	fallbackProv, ok := providers.Get("openai_compat")
	if !ok {
		return primary
	}
	fallbackModel := strings.TrimSpace(os.Getenv(CompatFallbackModelEnv))
	if fallbackModel == "" {
		fallbackModel = "llama3"
	}
	return NewFallback(
		[]Provider{primary, fallbackProv},
		[]string{primaryModelID, fallbackModel},
		FallbackOptions{},
	)
}
