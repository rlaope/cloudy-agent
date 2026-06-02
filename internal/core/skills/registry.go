package skills

import (
	"fmt"
	"sort"
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

// Suggest returns up to 3 skills whose Triggers match input
// (case-insensitive), ranked so that an exact trigger match outranks a mere
// substring match, with alphabetical name order as a stable tiebreaker. The
// ranking keeps a specific keyword like "gc" surfacing the skill whose trigger
// is exactly "gc" ahead of skills that only contain it as a substring (e.g. a
// "gcp" trigger), instead of letting the alphabetically-first three matches
// crowd it out of the cap.
func (r *Registry) Suggest(input string) []*Skill {
	lower := strings.ToLower(input)

	type scored struct {
		skill *Skill
		score int // 2 = exact trigger match, 1 = substring match
	}
	// r.items.All() is alphabetical and stable, so SliceStable below preserves
	// alphabetical order within a score tier.
	var ranked []scored
	for _, s := range r.items.All() {
		best := 0
		for _, t := range s.Triggers {
			lt := strings.ToLower(t)
			switch {
			case lt == lower:
				best = 2
			case best < 1 && strings.Contains(lt, lower):
				best = 1
			}
		}
		if best > 0 {
			ranked = append(ranked, scored{skill: s, score: best})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	matches := make([]*Skill, 0, 3)
	for _, sc := range ranked {
		if len(matches) == 3 {
			break
		}
		matches = append(matches, sc.skill)
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
