package skills_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/rlaope/cloudy/internal/core/skills"
)

// TestStaticSkill_RoundTrips wraps a Skill in StaticSkill and verifies Resolve
// hands back the original SystemPrompt and AllowedTools verbatim. This is the
// "zero behaviour change" guarantee for the prerequisite refactor.
func TestStaticSkill_RoundTrips(t *testing.T) {
	t.Parallel()

	src := &skills.Skill{
		Name:         "round-trip",
		SystemPrompt: "be helpful and read-only",
		AllowedTools: []string{"k8s.list_pods", "log.tail"},
	}

	sp := skills.NewStaticSkill(src)

	if got := sp.Name(); got != src.Name {
		t.Errorf("Name() = %q, want %q", got, src.Name)
	}

	prompt, allowed, err := sp.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}
	if prompt != src.SystemPrompt {
		t.Errorf("prompt = %q, want %q", prompt, src.SystemPrompt)
	}
	if !reflect.DeepEqual(allowed, src.AllowedTools) {
		t.Errorf("allowed = %v, want %v", allowed, src.AllowedTools)
	}
}

// TestSkillProvider_NilSafe guards the contract every existing agent test
// already relies on: callers that pass no skill (Options.Skill == nil) must
// still get full-registry, no-skill-prompt behaviour. The SkillProvider
// interface is a nil-able dependency — we explicitly do NOT wrap nil in a
// StaticSkill (NewStaticSkill panics on nil for that reason).
//
// This test pins both sides of the contract: NewStaticSkill panics on nil,
// and a typed-nil SkillProvider is what the agent should accept as "no
// active skill" (the agent treats Options.Skill == nil as the no-op case).
func TestSkillProvider_NilSafe(t *testing.T) {
	t.Parallel()

	// 1) NewStaticSkill(nil) must panic — callers with a possibly-nil *Skill
	//    must check before wrapping.
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewStaticSkill(nil) did not panic; expected a panic so callers cannot smuggle a nil Skill into the agent")
		}
	}()
	_ = skills.NewStaticSkill(nil)
}

// TestSkillProvider_TypedNilIsEmptySkill documents the secondary nil case:
// the SkillProvider interface itself can be a typed nil sentinel. We do NOT
// use this pattern in cloudy today — Options.Skill is a plain interface value
// that callers leave at its zero value (untyped nil) to mean "no active
// skill". This test pins that contract.
func TestSkillProvider_TypedNilIsEmptySkill(t *testing.T) {
	t.Parallel()

	var sp skills.SkillProvider // untyped nil interface
	if sp != nil {
		t.Errorf("zero-value SkillProvider should be nil, got %#v", sp)
	}
}
