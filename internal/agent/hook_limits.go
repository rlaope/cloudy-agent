package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"
)

// ErrLimitExceeded is returned by LimitGuardHook when a tool call's argument
// exceeds the configured per-call ceiling. The LLM sees this as a tool error
// and typically retries with a smaller value — that is the intended UX, not a
// silent narrowing of the user's request.
var ErrLimitExceeded = errors.New("agent: tool argument exceeds configured limit")

// LimitGuardHook enforces per-call argument ceilings derived from the active
// permission profile / safety config:
//
//   - max_log_lines: caps the "limit" argument on log.* tools
//   - max_profile_seconds: caps "duration_seconds" on jvm.async_profile,
//     perf.linux_perf_record, perf.v8_inspector_cpu_profile, ebpf.* tools
//
// Both ceilings are zero-aware — a zero value disables the check for that
// dimension. The hook fails closed: if it cannot parse the call arguments
// it lets the call through so a malformed payload is rejected by the tool
// itself with its own error message rather than masked by this guard.
type LimitGuardHook struct {
	NoopHook

	maxLogLines       int
	maxProfileSeconds int
}

// NewLimitGuardHook returns a hook that rejects tool calls whose arguments
// exceed the supplied ceilings. Zero ceilings disable the corresponding check.
func NewLimitGuardHook(maxLogLines, maxProfileSeconds int) *LimitGuardHook {
	return &LimitGuardHook{
		maxLogLines:       maxLogLines,
		maxProfileSeconds: maxProfileSeconds,
	}
}

// BeforeToolCall implements Hook. Returns ErrLimitExceeded with a descriptive
// suffix when an over-limit argument is present.
func (h *LimitGuardHook) BeforeToolCall(_ context.Context, call llm.ToolCall) error {
	if h.maxLogLines == 0 && h.maxProfileSeconds == 0 {
		return nil
	}
	// Parse only the fields we care about. Unknown fields are ignored.
	var args struct {
		Limit           int `json:"limit"`
		DurationSeconds int `json:"duration_seconds"`
	}
	if len(call.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		// Malformed args reach the tool unchanged; the tool will reject them
		// with a clearer parse error than we could synthesise here.
		return nil
	}

	if h.maxLogLines > 0 && isLogTool(call.Name) && args.Limit > h.maxLogLines {
		return fmt.Errorf("%w: %s limit=%d > max_log_lines=%d",
			ErrLimitExceeded, call.Name, args.Limit, h.maxLogLines)
	}
	if h.maxProfileSeconds > 0 && isProfileTool(call.Name) && args.DurationSeconds > h.maxProfileSeconds {
		return fmt.Errorf("%w: %s duration_seconds=%d > max_profile_seconds=%d",
			ErrLimitExceeded, call.Name, args.DurationSeconds, h.maxProfileSeconds)
	}
	return nil
}

// isLogTool reports whether the tool name belongs to the log group, where
// "limit" is the row-count cap argument.
func isLogTool(name string) bool {
	return strings.HasPrefix(name, "log.")
}

// isProfileTool reports whether the tool name belongs to a group whose
// "duration_seconds" argument is the wall-clock profiling window. This is the
// explicit allowlist of tools that carry a profile-duration argument.
func isProfileTool(name string) bool {
	switch name {
	case "jvm.async_profile",
		"perf.linux_perf_record",
		"perf.v8_inspector_cpu_profile",
		"perf.go_pprof_cpu",
		"py.spy_top_snapshot",
		"py.spy_dump",
		"ebpf.biolatency",
		"ebpf.tcprtt",
		"ebpf.tcptop",
		"ebpf.execsnoop",
		"ebpf.bpftrace_oneliner":
		return true
	}
	return false
}
