package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

// stubTool is a minimal Tool implementation for testing.
type stubTool struct {
	name     string
	readOnly bool
}

func (s stubTool) Name() string                   { return s.name }
func (s stubTool) Description() string            { return "stub: " + s.name }
func (s stubTool) Schema() json.RawMessage        { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) ReadOnly() bool                 { return s.readOnly }
func (s stubTool) Run(_ context.Context, _ json.RawMessage) (tools.Observation, error) {
	return tools.Observation{Text: "ok"}, nil
}

func TestRegistry_RegisterPanicsOnMutating(t *testing.T) {
	t.Parallel()
	r := tools.New()
	mutating := stubTool{name: "dangerous.delete", readOnly: false}

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic when registering a mutating tool, got none")
		}
	}()
	r.Register(mutating)
}

func TestRegistry_Filter(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(
		stubTool{name: "k8s.list_pods", readOnly: true},
		stubTool{name: "k8s.get_node", readOnly: true},
		stubTool{name: "prom.query", readOnly: true},
	)

	sub := r.Filter([]string{"k8s.*"})

	if _, ok := sub.Get("k8s.list_pods"); !ok {
		t.Error("expected k8s.list_pods in filtered registry")
	}
	if _, ok := sub.Get("k8s.get_node"); !ok {
		t.Error("expected k8s.get_node in filtered registry")
	}
	if _, ok := sub.Get("prom.query"); ok {
		t.Error("did not expect prom.query in filtered registry")
	}
}

func TestRegistry_List_StableOrder(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(
		stubTool{name: "z.tool", readOnly: true},
		stubTool{name: "a.tool", readOnly: true},
		stubTool{name: "m.tool", readOnly: true},
	)

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(list))
	}
	if list[0].Name() != "a.tool" || list[1].Name() != "m.tool" || list[2].Name() != "z.tool" {
		t.Errorf("unexpected order: %v %v %v", list[0].Name(), list[1].Name(), list[2].Name())
	}
}

func TestRegistry_ToolsFor(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(stubTool{name: "k8s.list_pods", readOnly: true})

	llmTools := r.ToolsFor("anthropic")
	if len(llmTools) != 1 {
		t.Fatalf("expected 1 llm.Tool, got %d", len(llmTools))
	}
	if llmTools[0].Name != "k8s.list_pods" {
		t.Errorf("unexpected tool name %q", llmTools[0].Name)
	}
}
