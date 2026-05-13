package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// ---------------------------------------------------------------------------
// Stub helpers
// ---------------------------------------------------------------------------

// stubProvider is a controllable llm.Provider that replays a fixed sequence
// of chunk slices — one slice per Stream() call.
type stubProvider struct {
	name   string
	rounds [][]llm.Chunk // each inner slice is consumed by one Stream() call
	call   int
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 16)
	round := p.call
	p.call++
	go func() {
		defer close(ch)
		if round >= len(p.rounds) {
			ch <- llm.Chunk{Done: true}
			return
		}
		for _, c := range p.rounds[round] {
			ch <- c
		}
	}()
	return ch, nil
}

// stubTool is a minimal read-only Tool.
type stubTool struct {
	name string
	obs  tools.Observation
	err  error
	// callCount is incremented on each Run invocation.
	callCount *int
}

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "stub tool" }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Run(_ context.Context, _ json.RawMessage) (tools.Observation, error) {
	if s.callCount != nil {
		*s.callCount++
	}
	return s.obs, s.err
}

// noopStream returns a render.Stream that discards output.
func noopStream() *render.Stream {
	return render.NewStream(io.Discard, render.NewTheme(true))
}

// textChunks builds a simple sequence of text chunks ending with Done.
func textChunks(text string) []llm.Chunk {
	return []llm.Chunk{
		{DeltaText: text},
		{Done: true},
	}
}

// toolCallChunks builds a chunk slice that asks the model to call a tool.
func toolCallChunks(id, name string, args json.RawMessage) []llm.Chunk {
	return []llm.Chunk{
		{ToolCall: &llm.ToolCall{ID: id, Name: name, Arguments: args}},
		{Done: true},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAgent_TextOnly(t *testing.T) {
	t.Parallel()

	prov := &stubProvider{
		name: "stub",
		rounds: [][]llm.Chunk{
			textChunks("Hello from cloudy."),
		},
	}

	reg := tools.New()
	a, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	msgs, err := a.Run(context.Background(), "ping", noopStream())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expect: system + user + assistant.
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant {
		t.Errorf("last message role = %q, want %q", last.Role, llm.RoleAssistant)
	}
	if last.Content != "Hello from cloudy." {
		t.Errorf("last message content = %q, want %q", last.Content, "Hello from cloudy.")
	}
}

func TestAgent_OneToolRoundTrip(t *testing.T) {
	t.Parallel()

	callCount := 0
	tool := stubTool{
		name:      "k8s.list_pods",
		obs:       tools.Observation{Text: "pod-a, pod-b"},
		callCount: &callCount,
	}

	prov := &stubProvider{
		name: "stub",
		rounds: [][]llm.Chunk{
			// Round 1: ask for tool call.
			toolCallChunks("call-1", "k8s.list_pods", json.RawMessage(`{"namespace":"default"}`)),
			// Round 2: final text after seeing the observation.
			textChunks("Found pods: pod-a, pod-b"),
		},
	}

	reg := tools.New()
	reg.Register(tool)

	a, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	msgs, err := a.Run(context.Background(), "list pods", noopStream())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if callCount != 1 {
		t.Errorf("tool called %d time(s), want 1", callCount)
	}

	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant {
		t.Errorf("last message role = %q, want %q", last.Role, llm.RoleAssistant)
	}
	if last.Content != "Found pods: pod-a, pod-b" {
		t.Errorf("last message content = %q", last.Content)
	}

	// Verify the tool-result message is present and contains the observation.
	var found bool
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.Content == "pod-a, pod-b" {
			found = true
		}
	}
	if !found {
		t.Error("expected a tool-result message containing the observation text")
	}
}

func TestAgent_UnknownToolDoesNotAbort(t *testing.T) {
	t.Parallel()

	prov := &stubProvider{
		name: "stub",
		rounds: [][]llm.Chunk{
			// Round 1: ask for an unknown tool.
			toolCallChunks("call-x", "missing.tool", json.RawMessage(`{}`)),
			// Round 2: model continues after error feedback.
			textChunks("I could not use that tool."),
		},
	}

	reg := tools.New() // empty registry — "missing.tool" won't be found
	a, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	msgs, err := a.Run(context.Background(), "do something", noopStream())
	if err != nil {
		t.Fatalf("expected Run to succeed despite unknown tool, got: %v", err)
	}

	last := msgs[len(msgs)-1]
	if last.Content != "I could not use that tool." {
		t.Errorf("unexpected final content: %q", last.Content)
	}
}

func TestAgent_MaxStepsGuard(t *testing.T) {
	t.Parallel()

	// Every round the model keeps asking for a tool call (never a final text).
	rounds := make([][]llm.Chunk, 20)
	for i := range rounds {
		// Use different call IDs so duplicate detection doesn't fire first.
		rounds[i] = toolCallChunks(
			"call-"+string(rune('a'+i)),
			"k8s.list_pods",
			json.RawMessage(`{"n":"`+string(rune('a'+i))+`"}`),
		)
	}

	prov := &stubProvider{name: "stub", rounds: rounds}
	reg := tools.New()
	reg.Register(stubTool{name: "k8s.list_pods", obs: tools.Observation{Text: "ok"}})

	a, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		Registry: reg,
		MaxSteps: 3,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = a.Run(context.Background(), "loop forever", noopStream())
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Errorf("expected ErrMaxSteps, got %v", err)
	}
}

func TestAgent_DuplicateDetection(t *testing.T) {
	t.Parallel()

	// Every round the model issues the exact same tool call.
	sameCall := toolCallChunks("call-dup", "k8s.list_pods", json.RawMessage(`{"namespace":"default"}`))
	rounds := make([][]llm.Chunk, 10)
	for i := range rounds {
		rounds[i] = sameCall
	}

	prov := &stubProvider{name: "stub", rounds: rounds}
	reg := tools.New()
	reg.Register(stubTool{name: "k8s.list_pods", obs: tools.Observation{Text: "pods"}})

	a, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		Registry: reg,
		MaxSteps: 20,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = a.Run(context.Background(), "list pods", noopStream())
	if !errors.Is(err, agent.ErrDuplicateCall) {
		t.Errorf("expected ErrDuplicateCall, got %v", err)
	}
}
