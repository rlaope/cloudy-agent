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

// readSessionEvents drains the .jsonl file at s.Path into a slice of
// decoded session.Events so tests can assert on the on-disk record.
func readSessionEvents(t *testing.T, s *session.Session) []session.Event {
	t.Helper()
	f, err := os.Open(s.Path)
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	defer f.Close()
	var out []session.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e session.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("decode event %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

// TestTuiSink_MirrorsToSession verifies the new wiring that mirrors tool
// boundaries, errors, and usage events to the session log so a post-mortem
// can see what failed without the operator scraping the TUI.
func TestTuiSink_MirrorsToSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLOUDY_HOME", "")

	sess, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()

	sink := &tuiSink{emit: func(AgentEvent) {}, sess: sess}

	sink.BeginToolCall("k8s.top_nodes", `{"context":"prod"}`)
	sink.EndToolCall("3 node(s)", nil)
	sink.BeginToolCall("k8s.top_pods", `{}`)
	sink.EndToolCall("", errors.New("k8s.top_pods: rbac forbidden"))
	sink.RecordUsage(llm.Usage{InputTokens: 100, OutputTokens: 42, CostUSD: 0.0012})

	events := readSessionEvents(t, sess)
	// 1 header + 2*(call+result/error) + 1 usage = 6
	if len(events) != 6 {
		t.Fatalf("want 6 events, got %d: %+v", len(events), events)
	}

	if events[0].Kind != session.KindSystem || events[0].Text != "opened" {
		t.Errorf("event[0] = %+v, want session opened", events[0])
	}
	if events[1].Kind != session.KindToolCall || events[1].Name != "k8s.top_nodes" {
		t.Errorf("event[1] = %+v, want tool_call k8s.top_nodes", events[1])
	}
	if string(events[1].Args) != `{"context":"prod"}` {
		t.Errorf("event[1].Args = %s, want raw args", string(events[1].Args))
	}
	if events[2].Kind != session.KindToolResult || events[2].Text != "3 node(s)" {
		t.Errorf("event[2] = %+v, want tool_result", events[2])
	}
	if events[3].Kind != session.KindToolCall || events[3].Name != "k8s.top_pods" {
		t.Errorf("event[3] = %+v", events[3])
	}
	if events[4].Kind != session.KindError || events[4].Name != "tool" {
		t.Errorf("event[4] = %+v, want kind=error name=tool", events[4])
	}
	if !strings.Contains(events[4].Text, "rbac forbidden") {
		t.Errorf("event[4].Text = %q, want to contain rbac forbidden", events[4].Text)
	}
	if events[5].Kind != session.KindUsage {
		t.Errorf("event[5].Kind = %q, want usage", events[5].Kind)
	}
	if events[5].Tokens == nil || events[5].Tokens.Input != 100 || events[5].Tokens.Output != 42 {
		t.Errorf("event[5].Tokens = %+v, want input=100 output=42", events[5].Tokens)
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
	// header + the one real failure
	if len(events) != 2 {
		t.Fatalf("want 2 events (header + real error), got %d: %+v", len(events), events)
	}
	if events[1].Kind != session.KindError || !strings.Contains(events[1].Text, "real failure") {
		t.Errorf("event[1] = %+v, want kind=error containing 'real failure'", events[1])
	}
}

