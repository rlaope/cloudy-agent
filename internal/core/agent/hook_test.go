package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// recordHook captures every Hook callback so tests can assert ordering.
type recordHook struct {
	agent.NoopHook
	before []string
	after  []string
	turns  int
	stop   error
}

func (h *recordHook) BeforeToolCall(_ context.Context, c llm.ToolCall) error {
	h.before = append(h.before, c.Name)
	return nil
}

func (h *recordHook) AfterToolCall(_ context.Context, c llm.ToolCall, obs tools.Observation, _ error) (tools.Observation, error) {
	h.after = append(h.after, c.Name)
	return obs, nil
}

func (h *recordHook) OnAssistantTurn(_ context.Context, _ llm.Message) { h.turns++ }
func (h *recordHook) OnStop(_ context.Context, err error)              { h.stop = err }

// abortHook returns sentinel from BeforeToolCall on its first invocation.
type abortHook struct {
	agent.NoopHook
	sentinel error
	tripped  bool
}

func (h *abortHook) BeforeToolCall(_ context.Context, _ llm.ToolCall) error {
	if h.tripped {
		return nil
	}
	h.tripped = true
	return h.sentinel
}

// maskHook rewrites any observation containing "secret" to "[redacted]".
type maskHook struct{ agent.NoopHook }

func (maskHook) AfterToolCall(_ context.Context, _ llm.ToolCall, obs tools.Observation, _ error) (tools.Observation, error) {
	if strings.Contains(obs.Text, "secret") {
		obs.Text = strings.ReplaceAll(obs.Text, "secret", "[redacted]")
	}
	return obs, nil
}

func TestHook_RecordsCallbackOrder(t *testing.T) {
	t.Parallel()
	prov := &stubProvider{
		name: "stub",
		rounds: [][]llm.Chunk{
			toolCallChunks("1", "demo.echo", json.RawMessage(`{}`)),
			textChunks("ok"),
		},
	}
	reg := tools.New()
	reg.MustRegister(stubTool{name: "demo.echo", obs: tools.Observation{Text: "fine"}})

	rec := &recordHook{}
	ag, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
		Hooks:    []agent.Hook{rec},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := ag.Run(context.Background(), "go", noopStream()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := rec.before; len(got) != 1 || got[0] != "demo.echo" {
		t.Errorf("before: %v", got)
	}
	if got := rec.after; len(got) != 1 || got[0] != "demo.echo" {
		t.Errorf("after: %v", got)
	}
	if rec.turns != 2 {
		t.Errorf("turns: want 2 (tool turn + final), got %d", rec.turns)
	}
	if rec.stop != nil {
		t.Errorf("stop: want nil, got %v", rec.stop)
	}
}

func TestHook_BeforeToolCallAborts(t *testing.T) {
	t.Parallel()
	prov := &stubProvider{
		name:   "stub",
		rounds: [][]llm.Chunk{toolCallChunks("1", "demo.echo", json.RawMessage(`{}`))},
	}
	reg := tools.New()
	reg.MustRegister(stubTool{name: "demo.echo"})

	sentinel := errors.New("policy: blocked")
	ag, _ := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
		Hooks:    []agent.Hook{&abortHook{sentinel: sentinel}},
	})
	_, err := ag.Run(context.Background(), "go", noopStream())
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestHook_AfterToolCallRewritesObservation(t *testing.T) {
	t.Parallel()
	prov := &stubProvider{
		name: "stub",
		rounds: [][]llm.Chunk{
			toolCallChunks("1", "demo.leak", json.RawMessage(`{}`)),
			textChunks("done"),
		},
	}
	reg := tools.New()
	reg.MustRegister(stubTool{name: "demo.leak", obs: tools.Observation{Text: "here is the secret value"}})

	ag, _ := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
		Hooks:    []agent.Hook{maskHook{}},
	})
	msgs, err := ag.Run(context.Background(), "go", noopStream())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var toolMsg *llm.Message
	for i := range msgs {
		if msgs[i].Role == llm.RoleTool {
			toolMsg = &msgs[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool result message found")
	}
	if strings.Contains(toolMsg.Content, "secret") {
		t.Errorf("tool message still contains 'secret': %q", toolMsg.Content)
	}
	if !strings.Contains(toolMsg.Content, "[redacted]") {
		t.Errorf("tool message missing [redacted]: %q", toolMsg.Content)
	}
}
