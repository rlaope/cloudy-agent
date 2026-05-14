package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeTool is a minimal Tool used only for registry-gate tests in this file.
type fakeTool struct{ name string }

func (f fakeTool) Name() string            { return f.name }
func (f fakeTool) Description() string     { return "test tool" }
func (f fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeTool) Run(_ interface{}, _ interface{}) (Observation, error) {
	return Observation{}, nil
}

// We need a Tool-satisfying impl with the real Run signature.
type readTool struct{ n string }

func (r readTool) Name() string            { return r.n }
func (r readTool) Description() string     { return "read tool" }
func (r readTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func TestRegister_AcceptsReadOnlyNames(t *testing.T) {
	cases := []string{
		"k8s.list_pods",
		"db.pg_stat_activity",
		"prom.query_range",
		"jvm.jcmd_gc",
		"log.loki_query_range",
		"db.redis_inspect_key",
		"db.mysql_top_table_size",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("read-only tool %q unexpectedly panicked: %v", name, r)
				}
			}()
			assertReadOnlyName(name)
		})
	}
}

func TestRegister_RejectsMutatorNames(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"k8s.create_pod", "create"},
		{"db.pg_update", "update"},
		{"k8s.delete_namespace", "delete"},
		{"k8s.exec_pod", "exec"},
		{"db.pg_drop_table", "drop"},
		{"k8s.scale_deployment", "scale"},
		{"k8s.restart_pod", "restart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic for mutator name %q", c.name)
				}
				err, ok := r.(error)
				if !ok {
					t.Fatalf("panic value not error: %T %v", r, r)
				}
				if !errors.Is(err, ErrMutatorTool) {
					t.Errorf("wrong error: got %v, want wraps ErrMutatorTool", err)
				}
				if !strings.Contains(err.Error(), c.token) {
					t.Errorf("error does not mention forbidden token %q: %v", c.token, err)
				}
			}()
			assertReadOnlyName(c.name)
		})
	}
}
