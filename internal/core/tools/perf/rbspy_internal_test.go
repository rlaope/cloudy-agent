package perf

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRBSpyDump_StubbedRunner(t *testing.T) {
	called := false
	prev := rbspyRunner
	rbspyRunner = func(ctx context.Context, args ...string) (string, string, error) {
		called = true
		if len(args) < 5 || args[0] != "dump" {
			t.Errorf("unexpected argv: %v", args)
		}
		if !strings.Contains(strings.Join(args, " "), "--pid 123") {
			t.Errorf("expected pid in argv, got %v", args)
		}
		return "thread 0\n  app/orders_controller.rb:42\n", "", nil
	}
	defer func() { rbspyRunner = prev }()

	tool := newRBSpyDumpTool()
	obs, err := tool.Run(context.Background(), json.RawMessage(`{"pid":123}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Error("runner not invoked")
	}
	if !strings.Contains(obs.Text, "orders_controller.rb") {
		t.Errorf("Text missing stubbed output: %q", obs.Text)
	}
}

func TestRBSpyDump_RejectsInvalidPID(t *testing.T) {
	tool := newRBSpyDumpTool()
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"pid":0}`)); err == nil {
		t.Error("expected error for pid=0")
	}
}
