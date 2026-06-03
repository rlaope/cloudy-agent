package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rlaope/cloudy/internal/incidentmemory"
)

// IncidentMemorySource returns a stable source reference for a session log.
func IncidentMemorySource(id string) (incidentmemory.Source, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return incidentmemory.Source{}, fmt.Errorf("session incident source: id required")
	}
	if id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\`) {
		return incidentmemory.Source{}, fmt.Errorf("session incident source: invalid id %q", id)
	}
	dir, err := logsDir()
	if err != nil {
		return incidentmemory.Source{}, fmt.Errorf("session incident source: %w", err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return incidentmemory.Source{}, fmt.Errorf("session incident source: session %s not found", id)
		}
		return incidentmemory.Source{}, fmt.Errorf("session incident source: stat %s: %w", path, err)
	}
	return incidentmemory.Source{Type: "session", ID: id, Path: path}, nil
}
