// Package secrets persists user-pasted credentials in a dotenv file under the
// cloudy home directory (mode 0600). On Load() each KEY=value pair is exported
// to the process environment so the existing *_env config fields keep working.
package secrets

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	keyRegex = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	mu       sync.RWMutex
	loaded   map[string]bool
)

func init() {
	loaded = make(map[string]bool)
}

// Path returns the resolved secrets file path. Resolution mirrors
// config.Path(): $CLOUDY_HOME → $XDG_CONFIG_HOME/cloudy → ~/.cloudy.
func Path() string {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "secrets")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cloudy", "secrets")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cloudy", "secrets")
	}
	return filepath.Join(home, ".cloudy", "secrets")
}

// Load reads the secrets file (if present) and calls os.Setenv for each entry.
// A missing file is not an error.
func Load() error {
	path := Path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets: read %s: %w", path, err)
	}

	mu.Lock()
	defer mu.Unlock()
	loaded = make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			// L-3 from the v0.5 security review: a typo'd line (no '=')
			// used to be skipped silently — operators with a truncated
			// API-key paste then ran unauthenticated without notice.
			// Warn to stderr while still proceeding with the rest of
			// the file so a single bad line does not lock the user out
			// of cloudy entirely.
			fmt.Fprintf(os.Stderr, "secrets: %s: skipping malformed line (no '='): %q\n", path, line)
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := parts[1]

		if key == "" {
			fmt.Fprintf(os.Stderr, "secrets: %s: skipping line with empty key: %q\n", path, line)
			continue
		}

		_ = os.Setenv(key, value)
		loaded[key] = true
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("secrets: scan %s: %w", path, err)
	}

	return nil
}

// Add writes (or overwrites) key=value in the secrets file using an atomic
// temp-file + rename, mode 0600. Parent directory is created with mode 0700.
// Also calls os.Setenv so the new value is immediately visible to the running
// process.
func Add(key, value string) error {
	// Validate key format
	if !keyRegex.MatchString(key) {
		return fmt.Errorf("secrets: invalid key %q: must match [A-Z_][A-Z0-9_]*", key)
	}

	// Reject multiline values
	if strings.Contains(value, "\n") || strings.Contains(value, "\r") {
		return fmt.Errorf("secrets: invalid value for %q: embedded newlines not supported", key)
	}

	path := Path()

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secrets: mkdir %s: %w", filepath.Dir(path), err)
	}

	// Read existing entries
	entries := make(map[string]string)
	if data, err := os.ReadFile(path); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			line = strings.TrimSpace(line)

			// Preserve comments and blank lines in order? No, we'll just recreate.
			// For now, preserve only KEY=VALUE pairs.
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				if k != "" {
					entries[k] = parts[1]
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("secrets: read %s: %w", path, err)
	}

	// Update with new entry
	entries[key] = value

	// Write to temp file then rename for atomicity
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloudy-secrets-*")
	if err != nil {
		return fmt.Errorf("secrets: create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Write all entries in sorted-key order. Without sorting the on-disk
	// file was shuffled on every Add() (map iteration order in Go is
	// randomized), defeating `git diff`-style review and chmod-time
	// integrity tracking for operators who back up ~/.cloudy/secrets.
	// L-2 from the v0.5 security review.
	sortedKeys := make([]string, 0, len(entries))
	for k := range entries {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, k := range sortedKeys {
		if _, err := fmt.Fprintf(tmp, "%s=%s\n", k, entries[k]); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return fmt.Errorf("secrets: write temp: %w", err)
		}
	}

	// Set permissions before closing
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("secrets: chmod temp: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("secrets: close temp: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("secrets: rename to %s: %w", path, err)
	}

	// Update process environment
	_ = os.Setenv(key, value)

	// Update loaded map
	mu.Lock()
	loaded[key] = true
	mu.Unlock()

	return nil
}

// Has reports whether key was loaded by the most recent Load() call.
func Has(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return loaded[key]
}
