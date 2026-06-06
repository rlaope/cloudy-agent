package chatops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SessionMap persists chat conversation keys to Cloudy session IDs.
type SessionMap struct {
	path string
	mu   sync.Mutex
}

// NewSessionMap constructs a session map at path, or the default
// ~/.cloudy/chatops/sessions.json path when path is empty.
func NewSessionMap(path string) *SessionMap {
	if path == "" {
		path = defaultSessionMapPath()
	}
	return &SessionMap{path: path}
}

// Path returns the underlying store path.
func (m *SessionMap) Path() string { return m.path }

// Lookup returns the mapped Cloudy session ID for an event conversation.
func (m *SessionMap) Lookup(ev Event) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := m.read()
	if err != nil {
		return ""
	}
	return data[conversationKey(ev)]
}

// Remember stores the Cloudy session ID for an event conversation.
func (m *SessionMap) Remember(ev Event, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := m.read()
	if err != nil {
		return err
	}
	data[conversationKey(ev)] = sessionID
	return m.write(data)
}

func (m *SessionMap) read() (map[string]string, error) {
	b, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chatops sessions: read %s: %w", m.path, err)
	}
	var data map[string]string
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("chatops sessions: parse %s: %w", m.path, err)
	}
	if data == nil {
		data = map[string]string{}
	}
	return data, nil
}

func (m *SessionMap) write(data map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("chatops sessions: mkdir %s: %w", filepath.Dir(m.path), err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("chatops sessions: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".sessions-*.json")
	if err != nil {
		return fmt.Errorf("chatops sessions: temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chatops sessions: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chatops sessions: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chatops sessions: close temp: %w", err)
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chatops sessions: rename: %w", err)
	}
	return nil
}

func conversationKey(ev Event) string {
	scope := firstNonEmpty(ev.WorkspaceID, ev.GuildID, ev.ChatID)
	thread := firstNonEmpty(ev.ThreadID, ev.ChannelID, ev.UserID)
	return strings.Join([]string{ev.Platform, scope, ev.ChannelID, thread, ev.UserID}, "|")
}

func defaultSessionMapPath() string {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "chatops", "sessions.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cloudy", "chatops", "sessions.json")
	}
	return filepath.Join(home, ".cloudy", "chatops", "sessions.json")
}
