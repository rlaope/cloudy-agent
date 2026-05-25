package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/session"
)

// readSessionEvents drains the .jsonl file at s.Path into a slice of decoded
// session.Events so tests can assert on the on-disk record. The scanner buffer
// is bumped to match Replay/readMeta so a future oversized event does not
// silently truncate the read.
func readSessionEvents(t *testing.T, s *session.Session) []session.Event {
	t.Helper()
	f, err := os.Open(s.Path)
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	const maxLine = 4 * 1024 * 1024
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	var out []session.Event
	for sc.Scan() {
		var e session.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("decode event %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan session log: %v", err)
	}
	return out
}

// TestTuiSink_MirrorsToSession pins the on-disk shape mirrored by the sink:
//   - KindToolCall with Name only (no Args yet — see tuiSink doc for the
//     deferred-until-masker rationale).
//   - No KindToolResult on success — success payloads are deliberately not
//     persisted until the masker is wired through this seam.
//   - KindError on failure carries the FAILING tool's name (not the
//     placeholder "tool" — that was a v1 of this PR which lost the tool id).
//   - KindUsage carries the model id in Event.Name so `session list` can
//     populate its Model column via readMeta.
func TestTuiSink_MirrorsToSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLOUDY_HOME", "")

	sess, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()

	sink := &tuiSink{emit: func(AgentEvent) {}, sess: sess, modelID: "anthropic/claude-sonnet-4-6"}

	sink.BeginToolCall("k8s.top_nodes", `{"context":"prod"}`)
	sink.EndToolCall("3 node(s)", nil)
	sink.BeginToolCall("k8s.top_pods", `{}`)
	sink.EndToolCall("", errors.New("k8s.top_pods: rbac forbidden"))
	sink.RecordUsage(llm.Usage{InputTokens: 100, OutputTokens: 42, CostUSD: 0.0012})

	events := readSessionEvents(t, sess)
	// header + 2 KindToolCall + 1 KindError + 1 KindUsage = 5
	if len(events) != 5 {
		t.Fatalf("want 5 events, got %d: %+v", len(events), events)
	}

	if events[0].Kind != session.KindSystem || events[0].Text != "opened" {
		t.Errorf("event[0] = %+v, want session opened", events[0])
	}
	if events[1].Kind != session.KindToolCall || events[1].Name != "k8s.top_nodes" {
		t.Errorf("event[1] = %+v, want tool_call k8s.top_nodes", events[1])
	}
	if len(events[1].Args) != 0 {
		t.Errorf("event[1].Args = %q, want empty (args not yet persisted)", string(events[1].Args))
	}
	if events[2].Kind != session.KindToolCall || events[2].Name != "k8s.top_pods" {
		t.Errorf("event[2] = %+v, want tool_call k8s.top_pods", events[2])
	}
	// EndToolCall(success) for top_nodes must NOT have appended a KindToolResult
	// between events[1] and events[2]; if it did, len(events) would be 6.

	if events[3].Kind != session.KindError {
		t.Errorf("event[3].Kind = %q, want error", events[3].Kind)
	}
	if events[3].Name != "k8s.top_pods" {
		t.Errorf("event[3].Name = %q, want the failing tool name (was placeholder %q in v1 of this PR)", events[3].Name, "tool")
	}
	if !strings.Contains(events[3].Text, "rbac forbidden") {
		t.Errorf("event[3].Text = %q, want to contain rbac forbidden", events[3].Text)
	}

	if events[4].Kind != session.KindUsage {
		t.Errorf("event[4].Kind = %q, want usage", events[4].Kind)
	}
	if events[4].Name != "anthropic/claude-sonnet-4-6" {
		t.Errorf("event[4].Name = %q, want the model id so readMeta can populate the Model column", events[4].Name)
	}
	if events[4].Tokens == nil || events[4].Tokens.Input != 100 || events[4].Tokens.Output != 42 {
		t.Errorf("event[4].Tokens = %+v, want input=100 output=42", events[4].Tokens)
	}
}

// TestLogSessionError_SkipsContextCanceled verifies that Ctrl+C cancellations
// do not pollute the error log — only real failures should be captured.
func TestLogSessionError_SkipsContextCanceled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLOUDY_HOME", "")

	sess, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()

	logSessionError(sess, "agent.run", context.Canceled)
	logSessionError(sess, "agent.run", nil)
	logSessionError(nil, "agent.run", errors.New("nil sess should be a no-op"))
	logSessionError(sess, "agent.run", errors.New("real failure"))

	events := readSessionEvents(t, sess)
	if len(events) != 2 {
		t.Fatalf("want 2 events (header + real error), got %d: %+v", len(events), events)
	}
	if events[1].Kind != session.KindError || !strings.Contains(events[1].Text, "real failure") {
		t.Errorf("event[1] = %+v, want kind=error containing 'real failure'", events[1])
	}
}

// TestTuiSink_EndToolCallSuccess_NoSessionWrite is a focused regression guard
// for the deferred-success-payload policy: a success-path EndToolCall MUST
// NOT write anything to the session (the masker is not yet routed through
// this seam, so persisting obs.Text would re-open the v0.5 M-1 gap).
func TestTuiSink_EndToolCallSuccess_NoSessionWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLOUDY_HOME", "")

	sess, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()

	sink := &tuiSink{emit: func(AgentEvent) {}, sess: sess, modelID: "m"}
	sink.BeginToolCall("k8s.list_pods", `{"namespace":"default"}`)
	sink.EndToolCall("3 pod(s)", nil)

	events := readSessionEvents(t, sess)
	// header + KindToolCall only — no KindToolResult
	if len(events) != 2 {
		t.Fatalf("want 2 events (header + tool_call), got %d: %+v", len(events), events)
	}
	for _, e := range events {
		if e.Kind == session.KindToolResult {
			t.Fatalf("unexpected KindToolResult on disk — masker not yet wired: %+v", e)
		}
	}
}
