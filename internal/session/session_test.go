package session_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/session"
)

// openSessionInDir opens a new Session whose log file lands in dir by
// temporarily overriding HOME so logsDir() points there.
func openSessionInDir(t *testing.T, dir string) *session.Session {
	t.Helper()
	t.Setenv("HOME", dir)
	// Also clear XDG so os.UserHomeDir() uses HOME.
	t.Setenv("XDG_CONFIG_HOME", "")

	s, err := session.New("")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	return s
}

func TestNew_AppendClose_Replay(t *testing.T) {
	dir := t.TempDir()
	s := openSessionInDir(t, dir)

	const n = 50
	for i := 0; i < n; i++ {
		err := s.Append(session.Event{
			Kind: session.KindUser,
			Text: fmt.Sprintf("message %d", i),
		})
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := session.Open(s.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// session.New writes a header event so /session list always has a
	// Started timestamp even for sessions that record nothing else.
	// Account for it: index 0 is the header, indices 1..n carry the
	// 50 appended user events.
	if len(r.Events) != n+1 {
		t.Errorf("event count: got %d, want %d (50 user + 1 header)", len(r.Events), n+1)
	}
	if r.Events[1].Text != "message 0" {
		t.Errorf("first user event text = %q, want %q", r.Events[1].Text, "message 0")
	}
	if r.Events[n].Text != fmt.Sprintf("message %d", n-1) {
		t.Errorf("last event text = %q", r.Events[n].Text)
	}
}

// TestNew_WritesOpeningHeader locks in the contract that session.New
// emits a "session opened" system event so empty TUI launches still
// produce a non-zero Started time in `cloudy session list`. Without
// this, readMeta sees zero events and leaves Meta.Started at the
// time.Time{} zero value, which prints as "0001-01-01 00:00:00".
func TestNew_WritesOpeningHeader(t *testing.T) {
	dir := t.TempDir()
	s := openSessionInDir(t, dir)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := session.Open(s.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(r.Events) != 1 {
		t.Fatalf("brand-new session should have exactly the header event, got %d", len(r.Events))
	}
	h := r.Events[0]
	if h.Kind != session.KindSystem || h.Name != "session" || h.Text != "opened" {
		t.Errorf("header event = %+v, want {Kind: system, Name: session, Text: opened}", h)
	}
	if h.Time.IsZero() {
		t.Error("header event must carry a non-zero timestamp — that's the whole point")
	}
}

func TestSession_IDGenerated(t *testing.T) {
	dir := t.TempDir()
	s := openSessionInDir(t, dir)
	defer s.Close()

	if s.ID == "" {
		t.Error("expected non-empty ID")
	}
	if s.Path == "" {
		t.Error("expected non-empty Path")
	}
}

func TestSession_EventTimestampSet(t *testing.T) {
	dir := t.TempDir()
	s := openSessionInDir(t, dir)
	defer s.Close()

	before := time.Now().UTC()
	if err := s.Append(session.Event{Kind: session.KindSystem, Text: "init"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after := time.Now().UTC()
	s.Close()

	r, _ := session.Open(s.Path)
	if len(r.Events) < 2 {
		t.Fatalf("expected header + appended event, got %d", len(r.Events))
	}
	// r.Events[0] is the session-opened header; the "init" we just
	// appended is at index 1. Verify its timestamp landed inside the
	// before/after window.
	ts := r.Events[1].Time
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v outside window [%v, %v]", ts, before, after)
	}
}

func TestList_ThreeSessions(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, ".cloudy", "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create 3 JSONL files with one event each, spaced 1 ms apart.
	ids := []string{"sess-a", "sess-b", "sess-c"}
	for i, id := range ids {
		path := filepath.Join(logsDir, id+".jsonl")
		ev := session.Event{
			Time: time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
			Kind: session.KindUser,
			Name: "test-model",
			Text: "hello",
		}
		data, _ := json.Marshal(ev)
		data = append(data, '\n')
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		// Stagger mtimes so sorting is deterministic.
		mtime := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	metas, err := session.List(logsDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("List returned %d metas, want 3", len(metas))
	}

	// Sorted newest first: sess-c has the latest mtime.
	if metas[0].ID != "sess-c" {
		t.Errorf("first meta ID = %q, want sess-c", metas[0].ID)
	}
	if metas[2].ID != "sess-a" {
		t.Errorf("last meta ID = %q, want sess-a", metas[2].ID)
	}
	for _, m := range metas {
		if m.EventCount != 1 {
			t.Errorf("meta %q: EventCount = %d, want 1", m.ID, m.EventCount)
		}
	}
}

func TestList_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	metas, err := session.List(dir)
	if err != nil {
		t.Fatalf("List on empty dir: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("expected 0 metas, got %d", len(metas))
	}
}

func TestList_MissingDir(t *testing.T) {
	dir := t.TempDir()
	metas, err := session.List(filepath.Join(dir, "nonexistent"))
	if err != nil {
		t.Fatalf("List on missing dir should not error: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("expected 0 metas, got %d", len(metas))
	}
}
