package session

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// TestSaveLoadHistoryRoundTrip verifies a snapshot survives Save→Load with
// messages and model intact, under an isolated $CLOUDY_HOME.
func TestSaveLoadHistoryRoundTrip(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())

	want := []llm.Message{
		{Role: llm.RoleUser, Content: "why is checkout slow"},
		{Role: llm.RoleAssistant, Content: "looking", ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "k8s_list_pods", Arguments: []byte(`{"namespace":"prod"}`)},
		}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "pod list"},
	}
	if err := SaveHistory("sess-1", "claude-3-5-sonnet-20241022", want); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	got, model, err := LoadHistory("sess-1")
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if model != "claude-3-5-sonnet-20241022" {
		t.Errorf("model = %q", model)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	if got[0].Content != want[0].Content || got[1].ToolCalls[0].Name != "k8s_list_pods" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

// TestLoadHistoryMissing distinguishes "no snapshot" from other errors.
func TestLoadHistoryMissing(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	_, _, err := LoadHistory("does-not-exist")
	if !errors.Is(err, ErrNoResumeState) {
		t.Errorf("err = %v, want ErrNoResumeState", err)
	}
}

// TestSaveHistoryFileMode verifies the snapshot file is 0600 — a
// world-readable resume file would defeat the masking entirely.
func TestSaveHistoryFileMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	if err := SaveHistory("sess-mode", "m", []llm.Message{{Role: llm.RoleUser, Content: "x"}}); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	info, err := os.Stat(home + "/sessions/sess-mode/history.json")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

// TestSaveHistoryPersistsVerbatim is a guard that SaveHistory itself does NOT
// redact — masking is the caller's job (permission.MaskHistory). This pins
// the contract so a future "helpful" masker added here doesn't silently
// double-mask or lull callers into skipping their mandatory pass.
func TestSaveHistoryPersistsVerbatim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	if err := SaveHistory("sess-raw", "m", []llm.Message{{Role: llm.RoleUser, Content: "token=KEEPME"}}); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	data, _ := os.ReadFile(home + "/sessions/sess-raw/history.json")
	if !strings.Contains(string(data), "KEEPME") {
		t.Errorf("SaveHistory should persist its input verbatim; masking is the caller's responsibility")
	}
}
