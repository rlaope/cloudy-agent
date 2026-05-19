// Package config manages user configuration for cloudy, loaded from
// ~/.cloudy/config.yaml (or $XDG_CONFIG_HOME/cloudy/config.yaml).
//
// Typical usage:
//
//	cfg, err := config.Load(config.Path())
//	if err != nil {
//	    log.Fatal(err)
//	}
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the top-level user configuration structure.
type Config struct {
	// DefaultModel is the model identifier used when no skill-level preference
	// overrides it (e.g. "claude-3-5-sonnet-20241022").
	DefaultModel string `yaml:"default_model"`

	// Providers holds per-provider API key and base-URL settings keyed by
	// provider name (e.g. "anthropic", "openai").
	Providers map[string]ProviderConfig `yaml:"providers"`

	// Prometheus is the list of Prometheus endpoints the agent may query.
	Prometheus []PrometheusEndpoint `yaml:"prometheus"`

	// Databases is the list of read-only database endpoints the agent may
	// query for diagnostic information (status, locks, slow queries, etc.).
	// Each entry's kind selects the wrapper tool group: postgres / mysql /
	// redis. Connection strings should point at a read-only user; cloudy
	// exposes only canonical read queries per backend.
	Databases []DatabaseEndpoint `yaml:"databases,omitempty"`

	// Logs is the list of log-search backends the agent may query. Each
	// entry's kind selects the wrapper: loki / elasticsearch. cloudy uses
	// only GET endpoints — transport-level read-only enforcement applies.
	Logs []HTTPEndpoint `yaml:"logs,omitempty"`

	// Tracing is the list of distributed-tracing backends. Each entry's kind
	// selects the wrapper: tempo / jaeger. cloudy uses only GET endpoints.
	Tracing []HTTPEndpoint `yaml:"tracing,omitempty"`

	// Pprof is the list of Go services exposing the standard /debug/pprof/*
	// HTTP endpoints. Each entry's URL is the base, e.g. http://api:6060.
	// cloudy uses only the text-formatted variants (?debug=1/2).
	Pprof []HTTPEndpoint `yaml:"pprof,omitempty"`

	// NodeInspectors is the list of Node.js processes running with the
	// V8 Inspector enabled (--inspect=...). cloudy queries the /json
	// discovery endpoint to enumerate debuggable targets; deeper
	// CPU/heap capture is deferred to a future release.
	NodeInspectors []HTTPEndpoint `yaml:"node_inspectors,omitempty"`

	// Contexts is the explicit list of kubeconfig contexts to expose to the
	// agent. Empty (or missing) means "use the kubeconfig current-context".
	Contexts []string `yaml:"contexts,omitempty"`

	// Safety contains guardrails that bound what the agent is allowed to do.
	Safety SafetyConfig `yaml:"safety"`

	// Routing controls the cheap-vs-strong model routing heuristics.
	Routing RoutingConfig `yaml:"routing"`
}

// ProviderConfig holds connection settings for a single LLM provider.
type ProviderConfig struct {
	// APIKeyEnv is the environment variable whose value is the API key.
	APIKeyEnv string `yaml:"api_key_env"`

	// BaseURL overrides the provider's default API base URL (optional).
	BaseURL string `yaml:"base_url,omitempty"`
}

// PrometheusEndpoint describes a single Prometheus instance the agent can
// query for metrics.
type PrometheusEndpoint struct {
	// Name is a human-readable label used in UI and logs.
	Name string `yaml:"name"`

	// URL is the base URL of the Prometheus HTTP API.
	URL string `yaml:"url"`

	// BasicUser is the HTTP Basic Auth username (optional).
	BasicUser string `yaml:"basic_user,omitempty"`

	// BasicPassEnv is the environment variable holding the Basic Auth password
	// (optional; preferred over storing the password in plain text).
	BasicPassEnv string `yaml:"basic_pass_env,omitempty"`

	// BearerEnv is the environment variable holding the Bearer token (optional).
	BearerEnv string `yaml:"bearer_env,omitempty"`
}

// HTTPEndpoint describes a single read-only HTTP backend (logs / tracing).
// Kind selects the wrapper tool group; URL is the base of the backend's
// HTTP API; auth fields are mutually exclusive (bearer wins when both set).
type HTTPEndpoint struct {
	// Name is a human-readable label used in UI and as the tool argument key.
	Name string `yaml:"name"`

	// Kind selects the backend: loki | elasticsearch | tempo | jaeger.
	Kind string `yaml:"kind"`

	// URL is the base URL of the backend's HTTP API.
	URL string `yaml:"url"`

	// BasicUser is the HTTP Basic Auth username (optional).
	BasicUser string `yaml:"basic_user,omitempty"`
	// BasicPassEnv is the env var holding the Basic Auth password (optional).
	BasicPassEnv string `yaml:"basic_pass_env,omitempty"`
	// BearerEnv is the env var holding a Bearer token (optional).
	BearerEnv string `yaml:"bearer_env,omitempty"`
}

// DatabaseEndpoint describes a single read-only database the agent can
// inspect. The Kind selects which tool wrapper applies; DSN format is
// driver-specific (postgres URL, mysql DSN, redis "host:port").
type DatabaseEndpoint struct {
	// Name is a human-readable label used in UI and as the tool argument key.
	Name string `yaml:"name"`

	// Kind selects the backend: "postgres" | "mysql" | "redis".
	Kind string `yaml:"kind"`

	// DSN is the connection string. Drivers:
	//   postgres: "postgres://user@host:5432/db?sslmode=disable"
	//   mysql:    "user@tcp(host:3306)/db"
	//   redis:    "host:6379" (no scheme), optionally with ?db=N
	// Use a read-only user. cloudy exposes only canonical read queries.
	DSN string `yaml:"dsn"`

	// PasswordEnv is the environment variable holding the connection
	// password, when the DSN does not embed it.
	PasswordEnv string `yaml:"password_env,omitempty"`
}

// SafetyConfig contains guardrails that bound agent behaviour.
type SafetyConfig struct {
	// AllowSecrets permits the agent to read Kubernetes Secrets. Defaults to
	// false to prevent accidental credential exposure.
	AllowSecrets bool `yaml:"allow_secrets"`

	// MaxLogLines caps the number of log lines fetched in a single tool call.
	MaxLogLines int `yaml:"max_log_lines"`

	// MaxProfileSeconds caps the duration of any profiling session in seconds.
	MaxProfileSeconds int `yaml:"max_profile_seconds"`

	// MaxTokensPerSession is the hard limit on tokens consumed in one session.
	// Zero means unlimited.
	MaxTokensPerSession int `yaml:"max_tokens_per_session"`

	// MaxUSDPerDay is the maximum spend (USD) allowed across all sessions in a
	// rolling 24-hour window. Zero means unlimited.
	MaxUSDPerDay float64 `yaml:"max_usd_per_day"`

	// MaxConversationSeconds caps the wall-clock time a single agent Run may
	// consume. Distinct from per-tool deadlines; a slow LLM step plus a slow
	// tool step can otherwise add up to many minutes. Zero means unlimited.
	MaxConversationSeconds int `yaml:"max_conversation_seconds"`

	// MaxLogResponseBytes is the byte ceiling beyond which a log.* tool
	// result is rewritten as a head/tail + exception-context summary before
	// the LLM sees it. Zero disables the summary step.
	MaxLogResponseBytes int `yaml:"max_log_response_bytes"`
}

// RoutingConfig controls how cloudy chooses between cheap and strong models.
type RoutingConfig struct {
	// CheapToolStrongSummary enables the two-step routing strategy where cheap
	// models execute tool calls and a strong model summarises results.
	CheapToolStrongSummary bool `yaml:"cheap_tool_strong_summary"`

	// CheapModel is the model identifier used for tool-execution steps.
	CheapModel string `yaml:"cheap_model"`

	// StrongModel is the model identifier used for reasoning and summarisation.
	StrongModel string `yaml:"strong_model"`
}

// Default returns a Config populated with conservative, ready-to-use defaults.
// DefaultModel is intentionally empty: hard-coding a specific model id here
// has bitten us before — model ids deprecate (claude-3-5-sonnet-20241022 now
// 404s on the Anthropic API) and operators end up with stale defaults written
// to disk before they have a chance to /login. The /login conversation owns
// model selection end-to-end; until the operator picks one, cloudy refuses
// to dispatch (see the setup gate in internal/tui/app.go).
//
// Routing.CheapModel / StrongModel are likewise empty until the operator
// configures their own routing tiers — picking these per provider would
// just multiply the stale-default problem.
func Default() Config {
	return Config{
		DefaultModel: "",
		Providers:    map[string]ProviderConfig{},
		Safety: SafetyConfig{
			AllowSecrets:        false,
			MaxLogLines:         5000,
			MaxProfileSeconds:   60,
			MaxLogResponseBytes: 64 * 1024,
		},
		Routing: RoutingConfig{},
	}
}

// Load reads the YAML file at path and merges its values over Default. If the
// file does not exist, Load returns Default() and a nil error, so callers can
// treat a missing file as "use defaults".
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg to path as YAML using an atomic write (temp file + rename)
// with 0600 permissions. Parent directories are created if necessary.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	// Write to a sibling temp file then rename for atomicity.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloudy-config-*.yaml")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: close temp: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: rename to %s: %w", path, err)
	}
	return nil
}

// Path returns the resolved path to the user's config file. Resolution order:
//  1. $CLOUDY_HOME/config.yaml — explicit cloudy home, used by bastion
//     deployments to give each shell user their own state directory.
//  2. $XDG_CONFIG_HOME/cloudy/config.yaml — XDG-conformant location.
//  3. ~/.cloudy/config.yaml — default.
func Path() string {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "config.yaml")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cloudy", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cloudy", "config.yaml")
	}
	return filepath.Join(home, ".cloudy", "config.yaml")
}
