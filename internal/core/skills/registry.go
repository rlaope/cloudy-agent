package skills

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
// (case-insensitive), ranked so that an exact trigger match outranks a natural
// language sentence that contains a trigger, which outranks picker-style
// partial matching against trigger text. The ranking keeps a specific keyword
// like "gc" surfacing the skill whose trigger is exactly "gc" without letting
// it match unrelated words such as "gcp".
func (r *Registry) Suggest(input string) []*Skill {
	lower := strings.TrimSpace(strings.ToLower(input))
	if lower == "" {
		return nil
	}

	type scored struct {
		skill *Skill
		score int // 3 = exact trigger match, 2 = input contains trigger, 1 = trigger contains input
	}
	// r.items.All() is alphabetical and stable, so SliceStable below preserves
	// alphabetical order within a score tier.
	var ranked []scored
	for _, s := range r.items.All() {
		best := 0
		for _, t := range s.Triggers {
			lt := strings.TrimSpace(strings.ToLower(t))
			if lt == "" {
				continue
			}
			switch {
			case lt == lower:
				best = 3
			case best < 2 && containsTrigger(lower, lt):
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

func containsTrigger(input, trigger string) bool {
	if hasTriggerSeparator(trigger) {
		return strings.Contains(input, trigger)
	}
	// Atomic triggers are intentionally stricter than phrase triggers. We
	// match "oom" in "oomkilled" because operators use the compound form, but
	// avoid matching it inside unrelated words like "boom".
	idx := strings.Index(input, trigger)
	for idx >= 0 {
		beforeOK := idx == 0 || isTriggerBoundary(runeBefore(input, idx))
		after := idx + len(trigger)
		afterOK := after == len(input) || len([]rune(trigger)) > 2 || isTriggerBoundary(runeAfter(input, after))
		if beforeOK && afterOK {
			return true
		}
		next := idx + len(trigger)
		if next >= len(input) {
			break
		}
		rel := strings.Index(input[next:], trigger)
		if rel < 0 {
			break
		}
		idx = next + rel
	}
	return false
}

func runeBefore(s string, byteIdx int) rune {
	r, _ := utf8.DecodeLastRuneInString(s[:byteIdx])
	return r
}

func runeAfter(s string, byteIdx int) rune {
	r, _ := utf8.DecodeRuneInString(s[byteIdx:])
	return r
}

func hasTriggerSeparator(s string) bool {
	for _, r := range s {
		if isTriggerBoundary(r) {
			return true
		}
	}
	return false
}

func isTriggerBoundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
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
