// Package session provides an append-only JSONL event log for a single cloudy
// agent session, plus helpers to list past sessions and replay their events.
//
// Session files are stored under ~/.cloudy/logs/<id>.jsonl and use O_APPEND so
// that concurrent writers (rare in practice) do not corrupt earlier events.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Kind constants for Event.Kind.
const (
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
	KindSystem     = "system"
	KindError      = "error"
	KindUsage      = "usage"
)

// Event is a single structured entry in the session log.
type Event struct {
	// Time is the wall-clock time when the event was recorded.
	Time time.Time `json:"t"`

	// Kind categorises the event (see Kind* constants).
	Kind string `json:"kind"`

	// Name carries contextual identifiers such as tool name or model name.
	Name string `json:"name,omitempty"`

	// Text is the human-readable content of the event (user message, assistant
	// reply, error text, etc.).
	Text string `json:"text,omitempty"`

	// Args is the raw JSON arguments for a tool call.
	Args json.RawMessage `json:"args,omitempty"`

	// Result is the raw JSON result returned by a tool.
	Result json.RawMessage `json:"result,omitempty"`

	// Tokens records token consumption for this event (typically Kind=usage).
	Tokens *Tokens `json:"tokens,omitempty"`

	// Meta carries arbitrary key-value pairs for extensibility.
	Meta map[string]any `json:"meta,omitempty"`
}

// Tokens summarises token consumption and estimated cost for one LLM turn.
type Tokens struct {
	Input  int     `json:"input"`
	Output int     `json:"output"`
	USD    float64 `json:"usd"`
}

// Meta is session-level metadata derived cheaply from the first and last lines
// of a session file. Returned by List.
type Meta struct {
	// ID is the session identifier (the file stem of the .jsonl file).
	ID string

	// Path is the absolute path to the .jsonl file.
	Path string

	// Started is the timestamp of the first event in the file.
	Started time.Time

	// Ended is the timestamp of the last event in the file (zero if empty).
	Ended time.Time

	// EventCount is the total number of events in the file.
	EventCount int

	// Model is the first model name seen in a usage or assistant event.
	Model string

	// ModTime is the file modification time, used for sorting.
	ModTime time.Time
}

// Session is an open append-only JSONL session log.
type Session struct {
	// ID uniquely identifies this session.
	ID string

	// Path is the absolute path of the underlying .jsonl file.
	Path string

	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// New opens (or creates) the session log for id under dir. If id is empty, a
// time-prefixed RFC4122-style identifier is generated. dir defaults to
// ~/.cloudy/logs when empty.
func New(id string) (*Session, error) {
	if id == "" {
		id = newID()
	}

	dir, err := logsDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", dir, err)
	}

	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}

	s := &Session{
		ID:   id,
		Path: path,
		f:    f,
	}
	s.enc = json.NewEncoder(f)
	return s, nil
}

// Append serialises e as a single JSON object followed by a newline. It is
// safe to call from multiple goroutines.
func (s *Session) Append(e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(e); err != nil {
		return fmt.Errorf("session: append: %w", err)
	}
	return nil
}

// Close flushes and closes the underlying file.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// List scans dir for .jsonl session files and returns one Meta per file,
// sorted by modification time descending (newest first). When dir is empty,
// ~/.cloudy/logs is used.
func List(dir string) ([]Meta, error) {
	if dir == "" {
		var err error
		dir, err = logsDir()
		if err != nil {
			return nil, err
		}
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: list %s: %w", dir, err)
	}

	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		id := e.Name()[:len(e.Name())-len(".jsonl")]

		fi, err := e.Info()
		if err != nil {
			continue
		}

		m, err := readMeta(id, path, fi.ModTime())
		if err != nil {
			continue
		}
		metas = append(metas, m)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].ModTime.After(metas[j].ModTime)
	})
	return metas, nil
}

// readMeta reads the first and last few lines of a JSONL file to build Meta
// without loading all events into memory.
func readMeta(id, path string, modTime time.Time) (Meta, error) {
	m := Meta{ID: id, Path: path, ModTime: modTime}

	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return m, err
	}

	m.EventCount = len(lines)
	if len(lines) == 0 {
		return m, nil
	}

	// Parse first event for Started.
	var first Event
	if json.Unmarshal([]byte(lines[0]), &first) == nil {
		m.Started = first.Time
		if first.Name != "" && m.Model == "" {
			m.Model = first.Name
		}
	}

	// Parse last event for Ended.
	var last Event
	if json.Unmarshal([]byte(lines[len(lines)-1]), &last) == nil {
		m.Ended = last.Time
	}

	// Scan up to 10 lines for a model name.
	for _, line := range lines {
		if m.Model != "" {
			break
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			if (ev.Kind == KindUsage || ev.Kind == KindAssistant) && ev.Name != "" {
				m.Model = ev.Name
			}
		}
	}

	return m, nil
}

// logsDir returns the path to the session log directory. It honours
// $CLOUDY_HOME (used by bastion / multi-user deployments) before falling
// back to ~/.cloudy/logs.
func logsDir() (string, error) {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: home dir: %w", err)
	}
	return filepath.Join(home, ".cloudy", "logs"), nil
}

// newID generates a time-prefixed pseudo-UUID suitable for session IDs.
// Format: <unix-nano-hex>-<pid-hex> (no external deps required).
func newID() string {
	now := time.Now().UnixNano()
	pid := os.Getpid()
	return fmt.Sprintf("%016x-%08x", now, pid)
}
