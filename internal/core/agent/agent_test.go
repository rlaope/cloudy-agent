package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
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

func TestNew_RegistryFnAndRegistryBothMissing(t *testing.T) {
	t.Parallel()

	prov := &stubProvider{name: "stub"}
	_, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "stub-model",
		// Neither Registry nor RegistryFn set.
	})
	if err == nil {
		t.Fatal("expected error when both Registry and RegistryFn are nil, got nil")
	}
	want := "agent: Registry or RegistryFn is required"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRun_UsesRegistryFn(t *testing.T) {
	t.Parallel()

	// Build two distinct registries with different tool catalogues.
	regA := tools.New()
	regA.Register(stubTool{name: "tool.alpha", obs: tools.Observation{Text: "alpha result"}})

	regB := tools.New()
	regB.Register(stubTool{name: "tool.beta", obs: tools.Observation{Text: "beta result"}})

	// current tracks which registry the closure returns.
	current := regA

	// Capture the tools slice sent to each Stream() call.
	type capturedReq struct {
		tools []llm.Tool
	}
	var reqs []capturedReq

	capturingProvider := &capturingStubProvider{
		name: "stub",
		onStream: func(req llm.Request) {
			reqs = append(reqs, capturedReq{tools: req.Tools})
		},
		// Round 0: text-only (no tool calls) — first Run.
		// Round 1: text-only (no tool calls) — second Run.
		rounds: [][]llm.Chunk{
			textChunks("first run done"),
			textChunks("second run done"),
		},
	}

	ag, err := agent.New(agent.Options{
		Provider:   capturingProvider,
		Model:      "stub-model",
		RegistryFn: func() *tools.Registry { return current },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First Run → should see tool.alpha.
	if _, err := ag.Run(context.Background(), "first", noopStream()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if len(reqs) < 1 {
		t.Fatal("expected at least one Stream call after Run 1")
	}
	if got := toolNames(reqs[0].tools); got != "tool.alpha" {
		t.Errorf("Run 1 tools = %q, want %q", got, "tool.alpha")
	}

	// Swap registry to regB.
	current = regB
	beforeRun2 := len(reqs)

	// Second Run → should see tool.beta.
	if _, err := ag.Run(context.Background(), "second", noopStream()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(reqs) <= beforeRun2 {
		t.Fatal("expected at least one Stream call after Run 2")
	}
	if got := toolNames(reqs[beforeRun2].tools); got != "tool.beta" {
		t.Errorf("Run 2 tools = %q, want %q", got, "tool.beta")
	}
}

// systemOf returns the RoleSystem message content from a captured request,
// or "" if none. The agent always puts the system prompt at index 0.
func systemOf(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role == llm.RoleSystem {
			return m.Content
		}
	}
	return ""
}

// TestRun_PlanDirective pins that Options.Plan gates the planning directive in
// the system prompt: present when on, absent when off (the default).
func TestRun_PlanDirective(t *testing.T) {
	t.Parallel()

	const marker = "## Investigation planning"

	run := func(plan bool) string {
		var sys string
		prov := &capturingStubProvider{
			name:     "stub",
			onStream: func(req llm.Request) { sys = systemOf(req.Messages) },
			rounds:   [][]llm.Chunk{textChunks("done")},
		}
		ag, err := agent.New(agent.Options{
			Provider: prov,
			Model:    "stub-model",
			Registry: tools.New(),
			Plan:     plan,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := ag.Run(context.Background(), "why is checkout slow?", noopStream()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return sys
	}

	if on := run(true); !strings.Contains(on, marker) {
		t.Errorf("Plan=true: system prompt missing %q", marker)
	}
	if off := run(false); strings.Contains(off, marker) {
		t.Errorf("Plan=false: system prompt must not carry the planning directive")
	}
}

func TestRun_SkillFilterStillApplied_WithRegistryFn(t *testing.T) {
	t.Parallel()

	reg := tools.New()
	reg.Register(stubTool{name: "allowed.tool", obs: tools.Observation{Text: "ok"}})
	reg.Register(stubTool{name: "blocked.tool", obs: tools.Observation{Text: "not ok"}})

	var lastTools []llm.Tool
	capProv := &capturingStubProvider{
		name: "stub",
		onStream: func(req llm.Request) {
			lastTools = req.Tools
		},
		rounds: [][]llm.Chunk{
			textChunks("done"),
		},
	}

	sk := &skills.Skill{Name: "filter-test", AllowedTools: []string{"allowed.tool"}}

	ag, err := agent.New(agent.Options{
		Provider:   capProv,
		Model:      "stub-model",
		RegistryFn: func() *tools.Registry { return reg },
		Skill:      skills.NewStaticSkill(sk),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := ag.Run(context.Background(), "hi", noopStream()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := toolNames(lastTools); got != "allowed.tool" {
		t.Errorf("filtered tools = %q, want %q", got, "allowed.tool")
	}
}

// ---------------------------------------------------------------------------
// Additional stub helpers for capturing tests
// ---------------------------------------------------------------------------

// capturingStubProvider is like stubProvider but calls onStream before each
// Stream() response so tests can inspect the llm.Request.
type capturingStubProvider struct {
	name     string
	onStream func(llm.Request)
	rounds   [][]llm.Chunk
	call     int
}

func (p *capturingStubProvider) Name() string { return p.name }

func (p *capturingStubProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if p.onStream != nil {
		p.onStream(req)
	}
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

// toolNames returns a comma-joined list of tool names for easy comparison.
func toolNames(ts []llm.Tool) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.Name
	}
	// sort for determinism
	sort.Strings(names)
	return strings.Join(names, ",")
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
