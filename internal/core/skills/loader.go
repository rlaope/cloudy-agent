package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// LoadBuiltin loads all skills embedded at compile time from the sibling
// builtin/ directory. It returns an error if any embedded file fails to parse.
func LoadBuiltin() ([]*Skill, error) {
	return loadFromFS(builtinFS, "builtin")
}

// LoadUser loads skills from dir (e.g. ~/.cloudy/skills). Files whose names
// begin with "." are ignored. Non-existent directories return an empty slice
// without error, allowing graceful degradation when no user skills are present.
func LoadUser(dir string) ([]*Skill, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: reading user skill dir %q: %w", dir, err)
	}

	var out []*Skill
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if filepath.Ext(name) != ".md" {
			continue
		}

		stem := strings.TrimSuffix(name, ".md")
		path := filepath.Join(dir, name)

		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("skills: reading %q: %w", path, err)
		}

		s, err := Parse(stem, raw)
		if err != nil {
			return nil, fmt.Errorf("skills: parsing %q: %w", path, err)
		}
		s.SourcePath = path
		out = append(out, s)
	}
	return out, nil
}

// Merge combines builtin and user skill slices. When a skill name appears in
// both, the user entry takes precedence (override semantics).
func Merge(builtin, user []*Skill) []*Skill {
	byName := make(map[string]*Skill, len(builtin)+len(user))
	for _, s := range builtin {
		byName[s.Name] = s
	}
	for _, s := range user {
		byName[s.Name] = s // user wins
	}

	out := make([]*Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	return out
}

// loadFromFS is the shared implementation for loading skills from any fs.FS.
func loadFromFS(fsys fs.FS, dir string) ([]*Skill, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("skills: reading embedded dir %q: %w", dir, err)
	}

	var out []*Skill
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".md" {
			continue
		}

		stem := strings.TrimSuffix(name, ".md")
		path := dir + "/" + name

		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("skills: reading embedded %q: %w", path, err)
		}

		s, err := Parse(stem, raw)
		if err != nil {
			return nil, fmt.Errorf("skills: parsing embedded %q: %w", path, err)
		}
		s.SourcePath = path
		out = append(out, s)
	}
	return out, nil
}
