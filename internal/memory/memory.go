// Package memory persists durable, cross-session facts about the operator's
// environment — cluster/context→environment mappings, naming conventions,
// normal baselines, recurring-incident root causes — in a single markdown file
// alongside cloudy's other state.
//
// Unlike the session resume snapshot (one conversation, overwritten each turn),
// memory is append-only and shared by every session: it is injected into the
// system prompt at the top of each run so the agent starts already knowing what
// it learned before. The agent writes to it through the memory.record tool; the
// operator may also edit memory.md by hand.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

// fileName is the memory file's basename inside the cloudy state directory.
const fileName = "memory.md"

// maxInjectBytes bounds how much memory is fed into the system prompt. memory.md
// itself may grow unbounded (the operator prunes it), but a runaway file must
// not silently inflate every request's token floor — so Load returns only the
// most recent maxInjectBytes, trimmed to a clean line boundary.
const maxInjectBytes = 8 << 10 // 8 KiB

// Path returns the resolved memory file path, kept alongside config.yaml in
// cloudy's state directory so it honours CLOUDY_HOME / XDG_CONFIG_HOME exactly
// like config.Path.
func Path() string {
	return filepath.Join(filepath.Dir(config.Path()), fileName)
}

// Load returns the durable memory ready for system-prompt injection: the file
// contents, tail-trimmed to maxInjectBytes on a line boundary. A missing file
// is not an error — it returns "" so a fresh install simply injects nothing.
func Load() (string, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("memory: read %s: %w", Path(), err)
	}
	s := string(data)
	if len(s) > maxInjectBytes {
		s = s[len(s)-maxInjectBytes:]
		// Drop the partial leading line the byte-level cut may have created so
		// the injected text always starts at a whole entry.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimSpace(s), nil
}

// Append records one durable fact as a dated markdown bullet. Empty/whitespace
// facts are ignored (returns nil). Internal whitespace is collapsed so one fact
// stays one bullet, keeping the dated-bullet structure scannable and the
// tail-trim in Load line-safe. The file and its parent directory are created on
// first write with 0700/0600 perms, matching the rest of cloudy's state.
func Append(fact string) error {
	fact = strings.Join(strings.Fields(fact), " ")
	if fact == "" {
		return nil
	}

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("memory: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	day := time.Now().UTC().Format("2006-01-02")
	if _, err := fmt.Fprintf(f, "- (%s) %s\n", day, fact); err != nil {
		return fmt.Errorf("memory: append: %w", err)
	}
	return nil
}
