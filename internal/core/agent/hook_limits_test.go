package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

func mustArgs(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestLimitGuard_LogLineUnderCap(t *testing.T) {
	h := NewLimitGuardHook(1000, 60)
	call := llm.ToolCall{
		Name:      "log.loki_query_range",
		Arguments: mustArgs(t, map[string]any{"query": "{a=\"b\"}", "limit": 500}),
	}
	if err := h.BeforeToolCall(context.Background(), call); err != nil {
		t.Errorf("under-cap should be allowed, got %v", err)
	}
}

func TestLimitGuard_LogLineOverCapBlocks(t *testing.T) {
	h := NewLimitGuardHook(1000, 0)
	call := llm.ToolCall{
		Name:      "log.loki_query_range",
		Arguments: mustArgs(t, map[string]any{"limit": 5000}),
	}
	err := h.BeforeToolCall(context.Background(), call)
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("got %v, want ErrLimitExceeded", err)
	}
}

func TestLimitGuard_NonLogToolUnaffected(t *testing.T) {
	h := NewLimitGuardHook(100, 0)
	// k8s tool has a "limit" arg too but isn't log.* — must pass.
	call := llm.ToolCall{
		Name:      "k8s.list_pods",
		Arguments: mustArgs(t, map[string]any{"limit": 9999}),
	}
	if err := h.BeforeToolCall(context.Background(), call); err != nil {
		t.Errorf("non-log tool should not be affected: %v", err)
	}
}

func TestLimitGuard_ProfileSecondsOverCapBlocks(t *testing.T) {
	h := NewLimitGuardHook(0, 30)
	call := llm.ToolCall{
		Name:      "jvm.async_profile",
		Arguments: mustArgs(t, map[string]any{"pid": 1, "duration_seconds": 60}),
	}
	err := h.BeforeToolCall(context.Background(), call)
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("got %v, want ErrLimitExceeded", err)
	}
}

func TestLimitGuard_ProfileToolUnderCap(t *testing.T) {
	h := NewLimitGuardHook(0, 30)
	call := llm.ToolCall{
		Name:      "jvm.async_profile",
		Arguments: mustArgs(t, map[string]any{"pid": 1, "duration_seconds": 15}),
	}
	if err := h.BeforeToolCall(context.Background(), call); err != nil {
		t.Errorf("under-cap should be allowed: %v", err)
	}
}

func TestLimitGuard_ZeroCapsDisabled(t *testing.T) {
	h := NewLimitGuardHook(0, 0)
	calls := []llm.ToolCall{
		{Name: "log.loki_query_range", Arguments: mustArgs(t, map[string]any{"limit": 1_000_000})},
		{Name: "jvm.async_profile", Arguments: mustArgs(t, map[string]any{"duration_seconds": 999})},
	}
	for _, c := range calls {
		if err := h.BeforeToolCall(context.Background(), c); err != nil {
			t.Errorf("zero caps should never block %s: %v", c.Name, err)
		}
	}
}

func TestLimitGuard_MalformedArgsPassThrough(t *testing.T) {
	h := NewLimitGuardHook(100, 30)
	call := llm.ToolCall{
		Name:      "log.loki_query_range",
		Arguments: json.RawMessage(`not valid json`),
	}
	if err := h.BeforeToolCall(context.Background(), call); err != nil {
		t.Errorf("malformed args should pass through to tool: %v", err)
	}
}
