package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Replay holds all events read from a JSONL session file and provides
// ordered replay to any consumer via a channel.
type Replay struct {
	// Events is the ordered slice of all events in the session.
	Events []Event
}

// Open reads all events from the JSONL file at path into memory. Trailing
// partial lines (e.g. from an interrupted write) are silently skipped so that
// a crashed session can still be replayed up to the last complete event.
func Open(path string) (*Replay, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("replay: open %s: %w", path, err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	// Allow lines up to 4 MiB to accommodate large tool results.
	const maxLine = 4 * 1024 * 1024
	buf := make([]byte, maxLine)
	scanner.Buffer(buf, maxLine)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Partial or corrupt line — skip silently.
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("replay: scan %s: %w", path, err)
	}

	return &Replay{Events: events}, nil
}

// Stream sends all events in order to out, then closes the channel. The caller
// is responsible for providing a buffered channel if blocking is undesirable.
func (r *Replay) Stream(out chan<- Event) {
	for _, e := range r.Events {
		out <- e
	}
	close(out)
}
