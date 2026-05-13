package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

// stubTool is a minimal Tool implementation for testing. ReadOnly is enforced
// at the transport layer (see internal/transport), not on the Tool interface,
// so this stub no longer carries that method.
type stubTool struct {
	name string
}

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "stub: " + s.name }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Run(_ context.Context, _ json.RawMessage) (tools.Observation, error) {
	return tools.Observation{Text: "ok"}, nil
}

func TestRegistry_DuplicateRegisterPanics(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(stubTool{name: "k8s.list_pods"})

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register(stubTool{name: "k8s.list_pods"})
}

func TestRegistry_Filter(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(
		stubTool{name: "k8s.list_pods"},
		stubTool{name: "k8s.get_node"},
		stubTool{name: "prom.query"},
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
		stubTool{name: "z.tool"},
		stubTool{name: "a.tool"},
		stubTool{name: "m.tool"},
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
	r.MustRegister(stubTool{name: "k8s.list_pods"})

	llmTools := r.ToolsFor("anthropic")
	if len(llmTools) != 1 {
		t.Fatalf("expected 1 llm.Tool, got %d", len(llmTools))
	}
	if llmTools[0].Name != "k8s.list_pods" {
		t.Errorf("unexpected tool name %q", llmTools[0].Name)
	}
}

func TestRegistry_InventoryMixesRegisteredAndSkipped(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(
		stubTool{name: "k8s.list_pods"},
		stubTool{name: "k8s.get_node"},
		stubTool{name: "jvm.jstat_gc"},
	)
	r.MarkSkipped("prom", "no prometheus endpoints configured")
	r.MarkSkipped("db", "redis-cli not on PATH")

	inv := r.Inventory()
	if len(inv.Groups) != 4 {
		t.Fatalf("expected 4 groups, got %d: %+v", len(inv.Groups), inv.Groups)
	}

	want := []struct {
		name    string
		skipped bool
		tools   []string
	}{
		{name: "db", skipped: true},
		{name: "jvm", tools: []string{"jvm.jstat_gc"}},
		{name: "k8s", tools: []string{"k8s.get_node", "k8s.list_pods"}},
		{name: "prom", skipped: true},
	}
	for i, w := range want {
		got := inv.Groups[i]
		if got.Name != w.name {
			t.Errorf("group[%d] name = %q, want %q", i, got.Name, w.name)
		}
		if got.Skipped != w.skipped {
			t.Errorf("group[%d] %q skipped = %v, want %v", i, w.name, got.Skipped, w.skipped)
		}
		if !w.skipped {
			if got.Reason != "" {
				t.Errorf("group[%d] %q registered should have no reason, got %q", i, w.name, got.Reason)
			}
			if len(got.Tools) != len(w.tools) {
				t.Errorf("group[%d] %q tools = %v, want %v", i, w.name, got.Tools, w.tools)
				continue
			}
			for j, name := range w.tools {
				if got.Tools[j] != name {
					t.Errorf("group[%d] %q tools[%d] = %q, want %q", i, w.name, j, got.Tools[j], name)
				}
			}
		}
	}
}

func TestRegistry_InventoryRegisteredOverridesSkipped(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MarkSkipped("k8s", "kubeconfig missing")
	r.MustRegister(stubTool{name: "k8s.list_pods"})

	inv := r.Inventory()
	if len(inv.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(inv.Groups))
	}
	g := inv.Groups[0]
	if g.Name != "k8s" || g.Skipped {
		t.Errorf("expected k8s registered (not skipped), got %+v", g)
	}
}

func TestRegistry_FilterPreservesSkipped(t *testing.T) {
	t.Parallel()
	r := tools.New()
	r.MustRegister(stubTool{name: "k8s.list_pods"})
	r.MarkSkipped("prom", "no prometheus endpoints configured")

	sub := r.Filter([]string{"k8s.*"})
	skipped := sub.Skipped()
	if skipped["prom"] == "" {
		t.Errorf("filtered registry lost skipped reason for prom: %+v", skipped)
	}
}
