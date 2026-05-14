package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/tools"
)

// Hook is a cross-cutting policy that observes (and may short-circuit) the
// ReAct loop. Implementations embed NoopHook to override only the methods
// they care about; this keeps cost guards, audit logging, masking, and
// duplicate-call detection out of the loop body.
//
// Hooks are invoked in the order they were registered. Returning an error
// from BeforeToolCall or AfterToolCall aborts the loop; a non-nil
// replacement Observation from AfterToolCall is what the LLM sees as the
// tool result.
type Hook interface {
	// BeforeToolCall fires just before the agent dispatches a tool.
	// Returning a non-nil error stops the loop.
	BeforeToolCall(ctx context.Context, call llm.ToolCall) error

	// AfterToolCall fires once the tool has returned. It may replace obs
	// (e.g. masking) and may return a non-nil error to stop the loop. The
	// returned obs is what is fed back to the model.
	AfterToolCall(ctx context.Context, call llm.ToolCall, obs tools.Observation, err error) (tools.Observation, error)

	// OnAssistantTurn fires once per assistant turn, after the assistant
	// message has been recorded but before tools are dispatched.
	OnAssistantTurn(ctx context.Context, msg llm.Message)

	// OnUsage fires every time the LLM provider reports usage data on a
	// streaming chunk. Returning a non-nil error stops the loop and is the
	// mechanism budget guards use to enforce per-session token / USD caps.
	OnUsage(ctx context.Context, u llm.Usage) error

	// OnStop fires once when Run is about to return, with the terminal
	// error (nil on a normal final response).
	OnStop(ctx context.Context, finalErr error)
}

// NoopHook is the zero-behavior Hook. Embed it in your own hook to satisfy
// Hook while overriding only the methods you care about.
//
// Note on AfterToolCall semantics: the err parameter carries the tool's own
// error (if any). Returning a non-nil error from AfterToolCall aborts the
// loop, so NoopHook returns nil — observing a tool's failure is not the
// same as a hook deciding to stop the loop.
type NoopHook struct{}

func (NoopHook) BeforeToolCall(context.Context, llm.ToolCall) error { return nil }
func (NoopHook) AfterToolCall(_ context.Context, _ llm.ToolCall, obs tools.Observation, _ error) (tools.Observation, error) {
	return obs, nil
}
func (NoopHook) OnAssistantTurn(context.Context, llm.Message)  {}
func (NoopHook) OnUsage(context.Context, llm.Usage) error      { return nil }
func (NoopHook) OnStop(context.Context, error)                 {}

// ErrDuplicateCall is returned when the same (tool, args) pair is invoked
// three times in a row, indicating the model is stuck in a loop.
var ErrDuplicateCall = errors.New("agent: duplicate tool call detected (model stuck in loop)")

// DupCallHook stops the loop when the same tool call recurs three times in
// a row. It is registered by default in Agent.New unless callers supply
// their own hook list.
type DupCallHook struct {
	NoopHook
	last  string
	count int
}

// NewDupCallHook returns a fresh DupCallHook.
func NewDupCallHook() *DupCallHook { return &DupCallHook{} }

// BeforeToolCall implements Hook.
func (h *DupCallHook) BeforeToolCall(_ context.Context, call llm.ToolCall) error {
	hash := hashCall(call.Name, call.Arguments)
	if hash == h.last {
		h.count++
		if h.count >= 3 {
			return ErrDuplicateCall
		}
	} else {
		h.last = hash
		h.count = 1
	}
	return nil
}

// hashCall returns a short fingerprint of (name, args) for duplicate detection.
func hashCall(name string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write(args)
	return fmt.Sprintf("%x", h.Sum(nil))
}
