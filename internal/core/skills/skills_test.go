package skills_test

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/skills"
)

// ---------------------------------------------------------------------------
// TestParse
// ---------------------------------------------------------------------------

const validDoc = `---
name: test-skill
description: A test skill for unit tests.
triggers:
  - test
  - unit
allowed_tools:
  - tool.alpha
  - tool.beta
defaults:
  model_preference:
    - claude-3-5-sonnet
examples:
  - "Run the test skill."
requires:
  - prometheus
---

This is the system prompt.
It describes what the agent should do.
`

func TestParse_Valid(t *testing.T) {
	s, err := skills.Parse("test-skill", []byte(validDoc))
	if err != nil {
		t.Fatalf("Parse returned unexpected error: %v", err)
	}
	if s.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", s.Name, "test-skill")
	}
	if s.Description != "A test skill for unit tests." {
		t.Errorf("Description = %q", s.Description)
	}
	if len(s.Triggers) != 2 || s.Triggers[0] != "test" {
		t.Errorf("Triggers = %v", s.Triggers)
	}
	if len(s.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v", s.AllowedTools)
	}
	if len(s.ModelPreference) != 1 || s.ModelPreference[0] != "claude-3-5-sonnet" {
		t.Errorf("ModelPreference = %v", s.ModelPreference)
	}
	if len(s.Examples) != 1 {
		t.Errorf("Examples = %v", s.Examples)
	}
	if len(s.Requires) != 1 || s.Requires[0] != "prometheus" {
		t.Errorf("Requires = %v", s.Requires)
	}
	if !strings.Contains(s.SystemPrompt, "system prompt") {
		t.Errorf("SystemPrompt = %q", s.SystemPrompt)
	}
}

func TestParse_MissingName(t *testing.T) {
	doc := strings.ReplaceAll(validDoc, "name: test-skill\n", "")
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
}

func TestParse_MismatchedName(t *testing.T) {
	_, err := skills.Parse("other-skill", []byte(validDoc))
	if err == nil {
		t.Fatal("expected error for mismatched name, got nil")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error message = %q, want 'does not match'", err.Error())
	}
}

func TestParse_MissingClosingDelimiter(t *testing.T) {
	doc := "---\nname: test-skill\ndescription: x\nallowed_tools:\n  - t\n"
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for missing closing ---, got nil")
	}
}

func TestParse_EmptySystemPrompt(t *testing.T) {
	doc := `---
name: test-skill
description: A test skill.
allowed_tools:
  - tool.alpha
---
`
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for empty system prompt, got nil")
	}
}

func TestParse_MissingDescription(t *testing.T) {
	doc := strings.ReplaceAll(validDoc, "description: A test skill for unit tests.\n", "")
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for missing description, got nil")
	}
}

func TestParse_EmptyAllowedTools(t *testing.T) {
	doc := strings.ReplaceAll(validDoc, "allowed_tools:\n  - tool.alpha\n  - tool.beta\n", "")
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for empty allowed_tools, got nil")
	}
}

func TestParse_NoLeadingDelimiter(t *testing.T) {
	doc := "name: test-skill\n---\n"
	_, err := skills.Parse("test-skill", []byte(doc))
	if err == nil {
		t.Fatal("expected error for missing leading ---, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestLoadBuiltin
// ---------------------------------------------------------------------------

func TestLoadBuiltin(t *testing.T) {
	ss, err := skills.LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin error: %v", err)
	}
	if len(ss) < 7 {
		t.Errorf("LoadBuiltin returned %d skills, want >= 7", len(ss))
	}
	// Every entry must re-parse cleanly (i.e. the embedded files are valid).
	for _, s := range ss {
		if s.Name == "" {
			t.Errorf("skill with empty name loaded")
		}
		if s.SystemPrompt == "" {
			t.Errorf("skill %q has empty SystemPrompt", s.Name)
		}
		if len(s.AllowedTools) == 0 {
			t.Errorf("skill %q has no AllowedTools", s.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMerge
// ---------------------------------------------------------------------------

func TestMerge_UserOverridesBuiltin(t *testing.T) {
	builtin := []*skills.Skill{
		{Name: "alpha", Description: "builtin alpha"},
		{Name: "beta", Description: "builtin beta"},
	}
	user := []*skills.Skill{
		{Name: "alpha", Description: "user alpha"},
		{Name: "gamma", Description: "user gamma"},
	}

	merged := skills.Merge(builtin, user)

	byName := make(map[string]*skills.Skill)
	for _, s := range merged {
		byName[s.Name] = s
	}

	if byName["alpha"].Description != "user alpha" {
		t.Errorf("user override did not win: got %q", byName["alpha"].Description)
	}
	if byName["beta"].Description != "builtin beta" {
		t.Errorf("builtin beta should be unchanged: got %q", byName["beta"].Description)
	}
	if _, ok := byName["gamma"]; !ok {
		t.Error("user-only skill 'gamma' missing from merged result")
	}
	if len(merged) != 3 {
		t.Errorf("merged len = %d, want 3", len(merged))
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Suggest
// ---------------------------------------------------------------------------

func TestRegistry_Suggest_GCSurfacesJvmGC(t *testing.T) {
	ss, err := skills.LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin: %v", err)
	}
	r := skills.New(ss)

	results := r.Suggest("gc")
	found := false
	for _, s := range results {
		if s.Name == "jvm-gc" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(results))
		for i, s := range results {
			names[i] = s.Name
		}
		t.Errorf("Suggest(\"gc\") did not return jvm-gc; got %v", names)
	}
}

func TestRegistry_Suggest_TopThree(t *testing.T) {
	ss := []*skills.Skill{
		{Name: "a", Triggers: []string{"foo"}},
		{Name: "b", Triggers: []string{"foo"}},
		{Name: "c", Triggers: []string{"foo"}},
		{Name: "d", Triggers: []string{"foo"}},
	}
	r := skills.New(ss)
	results := r.Suggest("foo")
	if len(results) > 3 {
		t.Errorf("Suggest returned %d results, want <= 3", len(results))
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Validate
// ---------------------------------------------------------------------------

func TestRegistry_Validate_UnknownTool(t *testing.T) {
	ss := []*skills.Skill{
		{Name: "x", AllowedTools: []string{"tool.known", "tool.unknown"}},
	}
	r := skills.New(ss)

	known := map[string]struct{}{
		"tool.known": {},
	}
	err := r.Validate(known)
	if err == nil {
		t.Fatal("expected validation error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "tool.unknown") {
		t.Errorf("error should mention unknown tool name; got: %v", err)
	}
}

func TestRegistry_Validate_AllKnown(t *testing.T) {
	ss := []*skills.Skill{
		{Name: "x", AllowedTools: []string{"tool.a", "tool.b"}},
	}
	r := skills.New(ss)

	known := map[string]struct{}{
		"tool.a": {},
		"tool.b": {},
	}
	if err := r.Validate(known); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestRegistry_Validate_NilKnown(t *testing.T) {
	ss := []*skills.Skill{
		{Name: "x", AllowedTools: []string{"tool.anything"}},
	}
	r := skills.New(ss)
	// nil known map should be a no-op.
	if err := r.Validate(nil); err != nil {
		t.Errorf("Validate(nil) should be no-op, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Get and List
// ---------------------------------------------------------------------------

func TestRegistry_GetAndList(t *testing.T) {
	ss, _ := skills.LoadBuiltin()
	r := skills.New(ss)

	list := r.List()
	if len(list) != len(ss) {
		t.Errorf("List len = %d, want %d", len(list), len(ss))
	}

	// Verify stable sort (each call returns same order).
	list2 := r.List()
	for i := range list {
		if list[i].Name != list2[i].Name {
			t.Errorf("List is not stable: pos %d = %q vs %q", i, list[i].Name, list2[i].Name)
		}
	}

	// Get a known skill.
	s, ok := r.Get("jvm-gc")
	if !ok {
		t.Fatal("Get(\"jvm-gc\") returned false")
	}
	if s.Name != "jvm-gc" {
		t.Errorf("Get name = %q", s.Name)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get(\"nonexistent\") returned true")
	}
}
