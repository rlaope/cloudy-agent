package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/permission"
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

// TestTuiSink_MirrorsToSession pins the on-disk shape mirrored by the sink
// after the v0.5 M-1 seam was closed:
//   - KindToolCall carries the masked Args blob.
//   - KindToolResult on success carries the masked observation text.
//   - KindError on failure carries the FAILING tool's name (not the
//     placeholder "tool" — that was a v1 of this PR which lost the tool id)
//     and no KindToolResult is written for the failed call.
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
	// header + tool_call(top_nodes) + tool_result(top_nodes) +
	// tool_call(top_pods) + error(top_pods) + usage = 6
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
		t.Errorf("event[1].Args = %q, want the (nil-masker) args persisted verbatim", string(events[1].Args))
	}
	if events[2].Kind != session.KindToolResult || events[2].Name != "k8s.top_nodes" {
		t.Errorf("event[2] = %+v, want tool_result k8s.top_nodes", events[2])
	}
	if events[2].Text != "3 node(s)" {
		t.Errorf("event[2].Text = %q, want the success observation", events[2].Text)
	}
	if events[3].Kind != session.KindToolCall || events[3].Name != "k8s.top_pods" {
		t.Errorf("event[3] = %+v, want tool_call k8s.top_pods", events[3])
	}

	if events[4].Kind != session.KindError {
		t.Errorf("event[4].Kind = %q, want error", events[4].Kind)
	}
	if events[4].Name != "k8s.top_pods" {
		t.Errorf("event[4].Name = %q, want the failing tool name (was placeholder %q in v1 of this PR)", events[4].Name, "tool")
	}
	if !strings.Contains(events[4].Text, "rbac forbidden") {
		t.Errorf("event[4].Text = %q, want to contain rbac forbidden", events[4].Text)
	}

	if events[5].Kind != session.KindUsage {
		t.Errorf("event[5].Kind = %q, want usage", events[5].Kind)
	}
	if events[5].Name != "anthropic/claude-sonnet-4-6" {
		t.Errorf("event[5].Name = %q, want the model id so readMeta can populate the Model column", events[5].Name)
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
	if len(events) != 2 {
		t.Fatalf("want 2 events (header + real error), got %d: %+v", len(events), events)
	}
	if events[1].Kind != session.KindError || !strings.Contains(events[1].Text, "real failure") {
		t.Errorf("event[1] = %+v, want kind=error containing 'real failure'", events[1])
	}
}

// TestTuiSink_EndToolCallSuccess_PersistsMaskedResult pins the closed seam:
// a success-path EndToolCall now writes a KindToolResult carrying the masked
// observation text.
func TestTuiSink_EndToolCallSuccess_PersistsMaskedResult(t *testing.T) {
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
	// header + KindToolCall + KindToolResult
	if len(events) != 3 {
		t.Fatalf("want 3 events (header + tool_call + tool_result), got %d: %+v", len(events), events)
	}
	last := events[2]
	if last.Kind != session.KindToolResult || last.Name != "k8s.list_pods" || last.Text != "3 pod(s)" {
		t.Fatalf("want tool_result k8s.list_pods '3 pod(s)', got %+v", last)
	}
}

// TestTuiSink_MasksSecretsBeforeDisk is the security regression for the closed
// seam: with a real (default-pattern) masker, neither a secret in the tool
// arguments nor one in the observation text may reach the on-disk session log
// in cleartext. This is the guarantee the v0.5 M-1 gap previously violated by
// dropping the payloads entirely; now we persist them, masked.
func TestTuiSink_MasksSecretsBeforeDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLOUDY_HOME", "")

	sess, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()

	sink := &tuiSink{
		emit:    func(AgentEvent) {},
		sess:    sess,
		modelID: "m",
		masker:  permission.MaskerOrDefault(nil), // baseline patterns
	}
	// Secret in args (key-named) and in the observation (value-pattern).
	sink.BeginToolCall("db.pg_stat_activity", `{"dsn":"postgres://u:hunter2@h","password":"hunter2"}`)
	sink.EndToolCall("connected with AKIAIOSFODNN7EXAMPLE token", nil)

	raw, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	blob := string(raw)
	for _, secret := range []string{"hunter2", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("secret %q leaked to disk unmasked:\n%s", secret, blob)
		}
	}
	if !strings.Contains(blob, "[REDACTED]") {
		t.Fatalf("expected a redaction marker on disk, got:\n%s", blob)
	}
}
