package skills

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/registry"
)

// Registry is an indexed view over a set of Skills, backed by the shared
// generic registry.Map. Domain methods (Suggest, Validate) live here; raw
// storage operations delegate to the generic map.
type Registry struct {
	items *registry.Map[*Skill]
}

// New constructs a Registry from the provided slice. Duplicate names are
// resolved by last-write-wins (callers should use Merge before New).
func New(skills []*Skill) *Registry {
	r := &Registry{
		items: registry.New[*Skill](func(s *Skill) string { return s.Name }),
	}
	for _, s := range skills {
		r.items.Replace(s)
	}
	return r
}

// Get returns the Skill with the given name, or false if not found.
func (r *Registry) Get(name string) (*Skill, bool) { return r.items.Get(name) }

// List returns all skills in stable alphabetical order by name.
func (r *Registry) List() []*Skill { return r.items.All() }

// Suggest returns up to 3 skills whose Triggers contain input as a
// case-insensitive substring. The returned slice is in alphabetical order.
func (r *Registry) Suggest(input string) []*Skill {
	lower := strings.ToLower(input)
	var matches []*Skill
	for _, s := range r.items.All() {
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
	for _, s := range r.items.All() {
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
