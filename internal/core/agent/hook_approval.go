package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// ErrApprovalDenied is returned when a high-risk tool call is rejected by
// the operator. The LLM sees this as a tool-side error and typically retries
// with a cheaper alternative — that is the intended feedback loop, not a
// loop-aborting fatal.
var ErrApprovalDenied = errors.New("agent: high-risk tool call denied")

// Approver decides whether a high-risk tool call may proceed. Implementations
// MUST honour ctx (the agent's run context); blocking past cancellation is a
// bug. Returning (false, nil) means the operator said no; returning a non-nil
// error means the approver itself failed and the call is treated as denied.
type Approver func(ctx context.Context, call llm.ToolCall) (bool, error)

// ApprovalHook gates calls into tools whose RiskOf is RiskHigh behind an
// Approver decision. Lower-risk calls pass through unchanged. The hook
// reads the registry lazily so registry hot-swaps (e.g. /setup running mid-
// session) are picked up automatically.
type ApprovalHook struct {
	NoopHook
	reg      func() *tools.Registry
	approver Approver
}

// NewApprovalHook returns an ApprovalHook. A nil reg or approver makes the
// hook a no-op; callers normally pass through agent.Options instead of
// constructing this directly.
func NewApprovalHook(reg func() *tools.Registry, approver Approver) *ApprovalHook {
	return &ApprovalHook{reg: reg, approver: approver}
}

// BeforeToolCall implements Hook.
func (h *ApprovalHook) BeforeToolCall(ctx context.Context, call llm.ToolCall) error {
	if h.reg == nil || h.approver == nil {
		return nil
	}
	reg := h.reg()
	if reg == nil {
		return nil
	}
	tool, ok := reg.Get(call.Name)
	if !ok {
		// Unknown tool — let the registry layer report it.
		return nil
	}
	if tools.RiskOf(tool) != tools.RiskHigh {
		return nil
	}
	approved, err := h.approver(ctx, call)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrApprovalDenied, call.Name, err)
	}
	if !approved {
		return fmt.Errorf("%w: %s", ErrApprovalDenied, call.Name)
	}
	return nil
}

// DenyApprover returns an Approver that refuses every high-risk call with
// a message pointing the LLM (and the user) at the interactive TUI. It is
// the right choice for non-interactive entry points like `cloudy ask` where
// no human is sitting at a prompt to consent.
//
// Returning an error rather than panicking is deliberate: the LLM sees the
// rejection as a tool-side failure and typically picks a lower-risk
// alternative on the next turn, which is exactly the desired feedback loop.
func DenyApprover() Approver {
	return func(_ context.Context, call llm.ToolCall) (bool, error) {
		return false, fmt.Errorf(
			"tool %s is rated RiskHigh and requires an interactive operator decision; "+
				"run `cloudy` (TUI) to approve, or choose a lower-risk tool",
			call.Name)
	}
}
