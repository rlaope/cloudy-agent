// Package skills loads, parses, and indexes domain playbooks (skills) that
// drive agent behaviour. Each skill is a markdown file with a YAML frontmatter
// block followed by a free-form system prompt.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is the parsed representation of a single domain playbook.
type Skill struct {
	// Name is the canonical identifier, e.g. "k8s-incident". Must match the
	// file stem of the source markdown file.
	Name string

	// Description is a one-line human-readable summary of the skill's purpose.
	Description string

	// Triggers is the list of keywords or phrases that should activate this
	// skill during intent matching.
	Triggers []string

	// AllowedTools is the whitelist of tool names this skill is permitted to
	// invoke. The Tool Registry milestone will validate these names.
	AllowedTools []string

	// ModelPreference is an ordered list of model identifiers the agent should
	// prefer when executing this skill. Earlier entries take higher priority.
	ModelPreference []string

	// Examples is a list of sample user utterances that illustrate when this
	// skill applies.
	Examples []string

	// Requires lists external service dependencies (e.g. "prometheus", "k8s")
	// that must be reachable for the skill to operate correctly.
	Requires []string

	// SystemPrompt is the playbook body — everything after the closing `---`
	// frontmatter delimiter. Leading and trailing blank lines are stripped.
	SystemPrompt string

	// SourcePath is the filesystem path from which this skill was loaded.
	// Empty for skills produced programmatically.
	SourcePath string
}

// frontmatter is the internal YAML structure decoded from the --- block.
type frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Triggers     []string `yaml:"triggers"`
	AllowedTools []string `yaml:"allowed_tools"`
	Defaults     struct {
		ModelPreference []string `yaml:"model_preference"`
	} `yaml:"defaults"`
	Examples []string `yaml:"examples"`
	Requires []string `yaml:"requires"`
}

// Parse decodes a skill markdown file. name is the expected skill name (derived
// from the file stem by the caller). raw is the full file content.
//
// The file format is:
//
//	---
//	<YAML frontmatter>
//	---
//	<system prompt body>
//
// Validation rules:
//   - The file must start with "---\n".
//   - The frontmatter must be closed by a second "---" line.
//   - frontmatter.name must equal name.
//   - description must be non-empty.
//   - system_prompt (the body) must be non-empty.
//   - allowed_tools must contain at least one entry.
func Parse(name string, raw []byte) (*Skill, error) {
	const delim = "---"

	content := string(raw)

	// The file must open with the frontmatter delimiter.
	if !strings.HasPrefix(content, delim+"\n") {
		return nil, errors.New("skills: file must begin with ---")
	}

	// Strip the leading "---\n".
	rest := content[len(delim)+1:]

	// Find the closing delimiter.
	closeIdx := strings.Index(rest, "\n"+delim)
	if closeIdx == -1 {
		return nil, errors.New("skills: missing closing --- for frontmatter")
	}

	yamlBlock := rest[:closeIdx]
	body := rest[closeIdx+1+len(delim):]

	var fm frontmatter
	if err := yaml.NewDecoder(bytes.NewBufferString(yamlBlock)).Decode(&fm); err != nil {
		return nil, fmt.Errorf("skills: invalid frontmatter YAML: %w", err)
	}

	// Validate required fields.
	if fm.Name == "" {
		return nil, errors.New("skills: frontmatter 'name' is required")
	}
	if fm.Name != name {
		return nil, fmt.Errorf("skills: frontmatter name %q does not match file stem %q", fm.Name, name)
	}
	if fm.Description == "" {
		return nil, errors.New("skills: frontmatter 'description' is required")
	}
	if len(fm.AllowedTools) == 0 {
		return nil, errors.New("skills: 'allowed_tools' must contain at least one entry")
	}

	systemPrompt := strings.TrimSpace(body)
	// Remove a leading newline that immediately follows the closing --- line.
	if systemPrompt == "" {
		return nil, errors.New("skills: system prompt body must not be empty")
	}

	return &Skill{
		Name:            fm.Name,
		Description:     fm.Description,
		Triggers:        fm.Triggers,
		AllowedTools:    fm.AllowedTools,
		ModelPreference: fm.Defaults.ModelPreference,
		Examples:        fm.Examples,
		Requires:        fm.Requires,
		SystemPrompt:    systemPrompt,
	}, nil
}
