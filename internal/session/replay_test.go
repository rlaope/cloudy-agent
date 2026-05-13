package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/session"
)

func TestReplay_Stream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	events := []session.Event{
		{Time: time.Now().UTC(), Kind: session.KindUser, Text: "hello"},
		{Time: time.Now().UTC(), Kind: session.KindAssistant, Text: "world"},
		{Time: time.Now().UTC(), Kind: session.KindUsage, Name: "my-model", Tokens: &session.Tokens{Input: 10, Output: 5, USD: 0.001}},
	}

	var data []byte
	for _, e := range events {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r, err := session.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(r.Events) != len(events) {
		t.Fatalf("event count: got %d, want %d", len(r.Events), len(events))
	}

	ch := make(chan session.Event, len(events))
	r.Stream(ch)

	var received []session.Event
	for e := range ch {
		received = append(received, e)
	}

	if len(received) != len(events) {
		t.Fatalf("streamed %d events, want %d", len(received), len(events))
	}
	if received[0].Text != "hello" {
		t.Errorf("first event text = %q, want hello", received[0].Text)
	}
	if received[2].Name != "my-model" {
		t.Errorf("third event name = %q, want my-model", received[2].Name)
	}
	if received[2].Tokens == nil || received[2].Tokens.Input != 10 {
		t.Errorf("third event tokens = %v", received[2].Tokens)
	}
}

func TestReplay_ToleratesPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.jsonl")

	good := session.Event{Time: time.Now().UTC(), Kind: session.KindUser, Text: "ok"}
	line, _ := json.Marshal(good)

	// Write one good line + one truncated line.
	data := append(line, '\n')
	data = append(data, []byte(`{"t":"2024-01-`)...) // truncated

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r, err := session.Open(path)
	if err != nil {
		t.Fatalf("Open should not error on partial line: %v", err)
	}
	if len(r.Events) != 1 {
		t.Errorf("event count: got %d, want 1", len(r.Events))
	}
}

func TestReplay_MissingFile(t *testing.T) {
	_, err := session.Open(filepath.Join(t.TempDir(), "no-file.jsonl"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}
