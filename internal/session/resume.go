package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// ErrNoResumeState is returned (wrapped) by LoadHistory when no resume
// snapshot exists for an id, letting callers tell "unknown id" apart from
// "id has no saved conversation yet".
var ErrNoResumeState = errors.New("session: no resume state")

const resumeStateVersion = 1

// resumeState is the on-disk schema for a resumable conversation snapshot.
// Unlike the append-only audit JSONL, this is a last-known-good snapshot the
// caller overwrites wholesale each turn.
type resumeState struct {
	Version  int           `json:"v"`
	ID       string        `json:"id"`
	Model    string        `json:"model"`
	SavedAt  time.Time     `json:"saved_at"`
	Messages []llm.Message `json:"messages"`
}

// sessionsDir returns ~/.cloudy/sessions (honouring $CLOUDY_HOME), the home
// of per-session resume snapshots. Kept separate from logsDir's audit JSONL
// so the append-only audit contract stays untouched.
func sessionsDir() (string, error) {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: home dir: %w", err)
	}
	return filepath.Join(home, ".cloudy", "sessions"), nil
}

// SaveHistory writes msgs as the resume snapshot for id, overwriting any
// prior snapshot atomically (temp file + rename) so a torn write never
// corrupts the last good state.
//
// SECURITY: the caller MUST pass an already-redacted slice — SaveHistory
// performs no masking. The in-memory conversation history is not reliably
// masked (raw user prompts and assistant prose are never touched), so
// callers redact via permission.MaskHistory before handing it here.
func SaveHistory(id, model string, msgs []llm.Message) error {
	if id == "" {
		return errors.New("session: SaveHistory needs a non-empty id")
	}
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	idDir := filepath.Join(dir, id)
	if err := os.MkdirAll(idDir, 0o700); err != nil {
		return fmt.Errorf("session: mkdir %s: %w", idDir, err)
	}

	data, err := json.Marshal(resumeState{
		Version:  resumeStateVersion,
		ID:       id,
		Model:    model,
		SavedAt:  time.Now().UTC(),
		Messages: msgs,
	})
	if err != nil {
		return fmt.Errorf("session: marshal resume state: %w", err)
	}

	// os.CreateTemp makes the file 0600; the rename is atomic on the same
	// filesystem so readers never observe a half-written snapshot.
	tmp, err := os.CreateTemp(idDir, "history-*.tmp")
	if err != nil {
		return fmt.Errorf("session: temp resume file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("session: write resume state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("session: close resume state: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(idDir, "history.json")); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("session: replace resume state: %w", err)
	}
	return nil
}

// LoadHistory reads the resume snapshot for id, returning its messages and
// the model the snapshot was saved under. Returns a wrapped ErrNoResumeState
// when no snapshot exists for id.
func LoadHistory(id string) ([]llm.Message, string, error) {
	if id == "" {
		return nil, "", errors.New("session: LoadHistory needs a non-empty id")
	}
	dir, err := sessionsDir()
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, id, "history.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: %s", ErrNoResumeState, id)
		}
		return nil, "", fmt.Errorf("session: read resume state: %w", err)
	}
	var st resumeState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, "", fmt.Errorf("session: decode resume state: %w", err)
	}
	return st.Messages, st.Model, nil
}
