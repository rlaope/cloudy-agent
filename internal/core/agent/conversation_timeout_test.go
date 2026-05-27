package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// slowProvider blocks until the ctx is cancelled, then returns an error
// channel reporting ctx.Err. Used to simulate a hung LLM step that should
// be cut short by the conversation deadline.
type slowProvider struct{ name string }

func (s slowProvider) Name() string { return s.name }
func (s slowProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- llm.Chunk{Err: ctx.Err()}
	}()
	return ch, nil
}

func TestAgent_ConversationDeadlineSurfacesAsTypedError(t *testing.T) {
	reg := tools.New()
	a, err := agent.New(agent.Options{
		Provider:               slowProvider{name: "slow"},
		Model:                  "stub-model",
		Registry:               reg,
		MaxConversationSeconds: 1, // very short for the test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sink := render.NewStream(io.Discard, render.NewTheme(true))
	start := time.Now()
	_, runErr := a.Run(context.Background(), "ping", sink)
	elapsed := time.Since(start)

	if runErr == nil {
		t.Fatal("expected an error when deadline elapses, got nil")
	}
	if !errors.Is(runErr, agent.ErrConversationTimeout) {
		t.Errorf("got %v, want ErrConversationTimeout", runErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("expected deadline to fire within seconds, took %s", elapsed)
	}
}

func TestAgent_ZeroDeadlineDoesNotWrap(t *testing.T) {
	// stub provider returns a final text chunk immediately.
	prov := okProvider{text: "hi"}
	reg := tools.New()
	a, err := agent.New(agent.Options{
		Provider:               prov,
		Model:                  "stub",
		Registry:               reg,
		MaxConversationSeconds: 0,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sink := render.NewStream(io.Discard, render.NewTheme(true))
	_, runErr := a.Run(context.Background(), "ping", sink)
	if runErr != nil {
		t.Fatalf("zero deadline should not cause errors: %v", runErr)
	}
}

// okProvider returns one text chunk then Done.
type okProvider struct{ text string }

func (p okProvider) Name() string { return "ok" }
func (p okProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{DeltaText: p.text}
	ch <- llm.Chunk{Done: true}
	close(ch)
	return ch, nil
}

// Ensure unused json import does not break compilation if removed.
var _ = json.Marshal
