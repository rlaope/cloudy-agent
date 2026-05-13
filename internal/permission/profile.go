// Package permission implements the v0.2 Permission Profile layer (Layer 2
// in the plan's 3-Layer Scope Model). A Profile narrows what an already
// read-only cloudy instance is allowed to do — which tools the LLM sees,
// which namespaces / contexts are visible, and what per-session limits apply.
//
// The RBAC ClusterRole (Layer 1) is the absolute upper bound; this package
// can only narrow it further, never widen it. The TUI's `/scope` command
// (Layer 3, future) is built on top of the same Profile type.
package permission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is the on-disk schema for a Permission Profile. A profile lives
// at $CLOUDY_HOME/profiles/<name>.yaml (or ~/.cloudy/profiles/<name>.yaml).
type Profile struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Contexts    []string   `yaml:"contexts,omitempty"` // empty = any context allowed
	Namespaces  Namespaces `yaml:"namespaces,omitempty"`
	Tools       Tools      `yaml:"tools,omitempty"`
	Limits      Limits     `yaml:"limits,omitempty"`
	Masking     Masking    `yaml:"masking,omitempty"`
}

// Namespaces is the per-namespace allow/deny pair. Both lists are glob
// patterns (trailing-* supported, plus full-match). Deny always wins.
type Namespaces struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// Tools is the tool-name allow/deny pair (e.g. "k8s.*", "jvm.async_profile").
// Deny always wins. An empty Allow means "every tool not in Deny".
type Tools struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// Masking controls field-level redaction applied to tool observations,
// JSON documents, and free-form text that the agent produces or receives.
// Patterns are applied case-insensitively for KeyRegex key matching.
type Masking struct {
	// KeyRegex masks any value whose key (case-insensitive substring or
	// regex) matches one of these patterns. Applied to JSON-like maps,
	// YAML manifests, and tool observations rendered as text.
	// Example: ["password","token","api_?key","secret"]
	KeyRegex []string `yaml:"key_regex,omitempty"`
	// ValueRegex masks any value whose stringified form matches one of
	// these regexes. Useful for catching JWTs, AWS access keys, etc.
	// Example: ["eyJ[A-Za-z0-9_=-]{20,}\\.", "AKIA[0-9A-Z]{16}"]
	ValueRegex []string `yaml:"value_regex,omitempty"`
}

// Limits caps potentially-expensive operations. Zero means "use the global
// default from config.SafetyConfig".
type Limits struct {
	MaxLogLines         int     `yaml:"max_log_lines,omitempty"`
	MaxProfileSeconds   int     `yaml:"max_profile_seconds,omitempty"`
	MaxTokensPerSession int     `yaml:"max_tokens_per_session,omitempty"`
	MaxUSDPerDay        float64 `yaml:"max_usd_per_day,omitempty"`
}

// ErrNotFound is returned by Load and Active when no matching profile
// exists. Callers can use errors.Is to detect it.
var ErrNotFound = errors.New("permission: profile not found")

// Dir returns the directory that holds permission profiles. It honours
// CLOUDY_HOME (the bastion override) and falls back to ~/.cloudy/profiles.
func Dir() string {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "profiles")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cloudy", "profiles")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cloudy", "profiles")
	}
	return filepath.Join(home, ".cloudy", "profiles")
}

// activeFile is the single-line marker file that records which profile
// is currently active. Co-located with Dir() so it inherits CLOUDY_HOME.
func activeFile() string {
	return filepath.Join(filepath.Dir(Dir()), "active_profile")
}

// Path returns the on-disk path for a named profile.
func Path(name string) string {
	return filepath.Join(Dir(), name+".yaml")
}

// Load reads a profile by name. Returns ErrNotFound when the file is missing.
func Load(name string) (*Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("permission: empty profile name")
	}
	b, err := os.ReadFile(Path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, fmt.Errorf("permission: read %s: %w", name, err)
	}
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("permission: parse %s: %w", name, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	if p.Name != name {
		return nil, fmt.Errorf("permission: profile name %q does not match filename %q", p.Name, name)
	}
	return &p, nil
}

// Save writes the profile atomically (tmp + rename), mode 0600.
func Save(p *Profile) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("permission: profile name is required")
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return fmt.Errorf("permission: mkdir %s: %w", Dir(), err)
	}
	final := Path(p.Name)
	tmp, err := os.CreateTemp(Dir(), "."+p.Name+".*.yaml")
	if err != nil {
		return fmt.Errorf("permission: tmp file: %w", err)
	}
	tmpName := tmp.Name()
	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		_ = tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("permission: encode: %w", err)
	}
	_ = enc.Close()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("permission: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("permission: close: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("permission: rename: %w", err)
	}
	return nil
}

// List returns the names of every available profile, in stable alphabetical
// order. A missing directory yields an empty slice without error.
func List() ([]string, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("permission: read %s: %w", Dir(), err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

// Active returns the name of the currently active profile. Resolution order:
// 1. $CLOUDY_PROFILE env var
// 2. the active_profile marker file
// Returns ErrNotFound when neither is set.
func Active() (string, error) {
	if v := os.Getenv("CLOUDY_PROFILE"); v != "" {
		return strings.TrimSpace(v), nil
	}
	b, err := os.ReadFile(activeFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("permission: read active_profile: %w", err)
	}
	name := strings.TrimSpace(string(b))
	if name == "" {
		return "", ErrNotFound
	}
	return name, nil
}

// SetActive records the named profile as active. The profile must exist.
func SetActive(name string) error {
	if _, err := Load(name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(activeFile()), 0o755); err != nil {
		return fmt.Errorf("permission: mkdir: %w", err)
	}
	return os.WriteFile(activeFile(), []byte(name+"\n"), 0o600)
}

// ClearActive removes the active-profile marker. Idempotent.
func ClearActive() error {
	err := os.Remove(activeFile())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("permission: clear: %w", err)
	}
	return nil
}

// LoadActive returns the active profile, or (nil, ErrNotFound) when none
// is set. This is the convenience helper the wiring layer uses.
func LoadActive() (*Profile, error) {
	name, err := Active()
	if err != nil {
		return nil, err
	}
	return Load(name)
}
