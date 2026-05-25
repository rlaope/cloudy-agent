package skills

import "context"

// SkillProvider resolves a skill at agent-run time, returning the system-prompt
// text and the tool whitelist the agent should expose for this run.
//
// The concrete Skill struct ships today as StaticSkill — its Resolve simply
// returns the parsed frontmatter fields verbatim. Future implementations
// (RAGSkill, RunbookSkill, ...) will plug in behind the same interface so the
// agent loop never needs to learn about retrieval, runbook lookup, or any
// other late-binding mechanism. See docs/RFC-RAG.md §4 for the design rationale.
type SkillProvider interface {
	// Name is the canonical identifier of the resolved skill (e.g. "k8s-incident").
	// Used by the agent for logging and prompt assembly ("## Active skill: <name>").
	Name() string

	// Resolve returns the system-prompt prelude and the tool whitelist for this
	// run. allowed of zero length means "no skill-level filtering" — the agent
	// keeps the registry as-is. An error from Resolve aborts the Run.
	Resolve(ctx context.Context) (prompt string, allowed []string, err error)
}

// StaticSkill adapts a parsed *Skill to the SkillProvider interface. It is the
// trivial implementation: Resolve returns the frontmatter-derived fields with
// no I/O, no error path, no context use.
//
// Every call site that today hands the agent a *Skill should wrap it in a
// StaticSkill via NewStaticSkill before passing to agent.Options.
type StaticSkill struct {
	// S is the underlying parsed skill. Must be non-nil; NewStaticSkill enforces
	// this so the zero-value StaticSkill is never accidentally usable.
	S *Skill
}

// NewStaticSkill wraps s in a StaticSkill. Panics if s is nil — call sites
// that may have a nil *Skill must check before wrapping (the historical
// "no active skill" path leaves agent.Options.Skill nil entirely).
func NewStaticSkill(s *Skill) *StaticSkill {
	if s == nil {
		panic("skills: NewStaticSkill called with nil *Skill")
	}
	return &StaticSkill{S: s}
}

// Name returns the underlying skill's canonical name.
func (s *StaticSkill) Name() string { return s.S.Name }

// Resolve returns the frontmatter-derived SystemPrompt and AllowedTools.
// The context is intentionally unused — static skills do no I/O.
func (s *StaticSkill) Resolve(_ context.Context) (string, []string, error) {
	return s.S.SystemPrompt, s.S.AllowedTools, nil
}
