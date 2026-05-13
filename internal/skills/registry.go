package skills

import (
	"fmt"
	"sort"
	"strings"
)

// Registry is an indexed, read-only view over a set of Skills.
type Registry struct {
	byName  map[string]*Skill
	ordered []*Skill // stable alphabetical order by Name
}

// New constructs a Registry from the provided slice. Duplicate names are
// resolved by last-write-wins (callers should use Merge before New).
func New(skills []*Skill) *Registry {
	r := &Registry{
		byName: make(map[string]*Skill, len(skills)),
	}
	for _, s := range skills {
		r.byName[s.Name] = s
	}

	// Build a stable ordered slice.
	r.ordered = make([]*Skill, 0, len(r.byName))
	for _, s := range r.byName {
		r.ordered = append(r.ordered, s)
	}
	sort.Slice(r.ordered, func(i, j int) bool {
		return r.ordered[i].Name < r.ordered[j].Name
	})

	return r
}

// Get returns the Skill with the given name, or false if not found.
func (r *Registry) Get(name string) (*Skill, bool) {
	s, ok := r.byName[name]
	return s, ok
}

// List returns all skills in stable alphabetical order by name.
func (r *Registry) List() []*Skill {
	out := make([]*Skill, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// Suggest returns up to 3 skills whose Triggers contain input as a
// case-insensitive substring. The returned slice is in alphabetical order.
func (r *Registry) Suggest(input string) []*Skill {
	lower := strings.ToLower(input)
	var matches []*Skill
	for _, s := range r.ordered {
		for _, t := range s.Triggers {
			if strings.Contains(strings.ToLower(t), lower) {
				matches = append(matches, s)
				break
			}
		}
		if len(matches) == 3 {
			break
		}
	}
	return matches
}

// Validate checks that every tool name referenced in any skill's AllowedTools
// is present in known. It returns a combined error listing all unknown tools.
// This method is intentionally a no-op when known is nil, allowing callers to
// skip validation when the Tool Registry has not yet been initialised.
func (r *Registry) Validate(known map[string]struct{}) error {
	if known == nil {
		return nil
	}

	var errs []string
	for _, s := range r.ordered {
		for _, tool := range s.AllowedTools {
			if _, ok := known[tool]; !ok {
				errs = append(errs, fmt.Sprintf("skill %q references unknown tool %q", s.Name, tool))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("skills: validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
