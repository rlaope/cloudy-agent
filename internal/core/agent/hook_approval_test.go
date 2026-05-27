package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
)

type fakeLowRisk struct{}

func (fakeLowRisk) Name() string            { return "k8s.list_pods" }
func (fakeLowRisk) Description() string     { return "" }
func (fakeLowRisk) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (fakeLowRisk) Run(context.Context, json.RawMessage) (tools.Observation, error) {
	return tools.Observation{}, nil
}

type fakeProfile struct{}

func (fakeProfile) Name() string            { return "jvm.async_profile" }
func (fakeProfile) Description() string     { return "" }
func (fakeProfile) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (fakeProfile) Run(context.Context, json.RawMessage) (tools.Observation, error) {
	return tools.Observation{}, nil
}

func newRegistryWith(ts ...tools.Tool) *tools.Registry {
	r := tools.New()
	for _, t := range ts {
		r.Register(t)
	}
	return r
}

func TestApprovalHook_LowRiskBypassesApprover(t *testing.T) {
	reg := newRegistryWith(fakeLowRisk{})
	called := false
	h := NewApprovalHook(func() *tools.Registry { return reg }, func(context.Context, llm.ToolCall) (bool, error) {
		called = true
		return true, nil
	})
	err := h.BeforeToolCall(context.Background(), llm.ToolCall{Name: "k8s.list_pods", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("unexpected error for low-risk tool: %v", err)
	}
	if called {
		t.Fatalf("approver consulted for low-risk tool")
	}
}

func TestApprovalHook_HighRiskCallsApprover_Allow(t *testing.T) {
	reg := newRegistryWith(fakeProfile{})
	h := NewApprovalHook(func() *tools.Registry { return reg }, func(context.Context, llm.ToolCall) (bool, error) {
		return true, nil
	})
	if err := h.BeforeToolCall(context.Background(), llm.ToolCall{Name: "jvm.async_profile"}); err != nil {
		t.Fatalf("approved call still failed: %v", err)
	}
}

func TestApprovalHook_HighRiskCallsApprover_Deny(t *testing.T) {
	reg := newRegistryWith(fakeProfile{})
	h := NewApprovalHook(func() *tools.Registry { return reg }, func(context.Context, llm.ToolCall) (bool, error) {
		return false, nil
	})
	err := h.BeforeToolCall(context.Background(), llm.ToolCall{Name: "jvm.async_profile"})
	if !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("expected ErrApprovalDenied, got %v", err)
	}
}

func TestApprovalHook_NilGettersAreNoop(t *testing.T) {
	h := NewApprovalHook(nil, nil)
	if err := h.BeforeToolCall(context.Background(), llm.ToolCall{Name: "jvm.async_profile"}); err != nil {
		t.Fatalf("nil hook should be a no-op, got %v", err)
	}
}

func TestApprovalHook_UnknownToolPasses(t *testing.T) {
	reg := newRegistryWith(fakeLowRisk{})
	denied := false
	h := NewApprovalHook(func() *tools.Registry { return reg }, func(context.Context, llm.ToolCall) (bool, error) {
		denied = true
		return false, nil
	})
	// Unknown tool — ApprovalHook should defer to the registry layer.
	if err := h.BeforeToolCall(context.Background(), llm.ToolCall{Name: "nonexistent.tool"}); err != nil {
		t.Fatalf("unknown tool should pass approval, got %v", err)
	}
	if denied {
		t.Fatalf("approver consulted for unknown tool")
	}
}

func TestDenyApprover_AlwaysRefuses(t *testing.T) {
	a := DenyApprover()
	ok, err := a(context.Background(), llm.ToolCall{Name: "jvm.async_profile"})
	if ok {
		t.Fatalf("DenyApprover returned true")
	}
	if err == nil {
		t.Fatalf("DenyApprover returned nil error")
	}
}
