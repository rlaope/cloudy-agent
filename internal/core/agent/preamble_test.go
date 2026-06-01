package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// capturingProvider records the llm.Request it receives once, then returns a
// trivial Done chunk so the agent loop terminates immediately. Used to peek
// at the system prompt the agent assembled.
type capturingProvider struct {
	captured llm.Request
}

func (p *capturingProvider) Name() string { return "capturing" }

func (p *capturingProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	p.captured = req
	ch := make(chan llm.Chunk, 1)
	go func() {
		ch <- llm.Chunk{DeltaText: "done", Done: true}
		close(ch)
	}()
	return ch, nil
}

// systemMessage walks the captured request and returns the first System-role
// message body — the place the agent puts basePreamble + skill catalog +
// tool catalog.
func systemMessage(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			return m.Content
		}
	}
	return ""
}

// TestSystemPreamble_SelfKnowledge pins the regression that prompted this
// test: a user typed "what is /setup?" into the TUI and the LLM said "I
// don't know that term" because the preamble never named cloudy's surface.
// Every phrase asserted here is something the LLM should be able to answer
// from in-band context with no tool calls.
func TestSystemPreamble_SelfKnowledge(t *testing.T) {
	prov := &capturingProvider{}
	reg := tools.New()
	ag, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag.Run(context.Background(), "hi", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := systemMessage(prov.captured)
	required := []string{
		"cloudy",
		"read-only",
		"/setup",
		"/login",
		"/skill",
		"/scope",
		"/tools",
		"/update",
		"~/.cloudy",
		"Skills",
		"Tool-use rules",
		// rule 5: a clean verdict must rest on signals that actually returned
		// data — don't present "all-clear" when the diagnostic tools failed.
		"unverified",
		"cannot determine",
	}
	for _, want := range required {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing required phrase %q\n--- prompt ---\n%s\n--- end ---", want, sys)
		}
	}
}

// TestSystemPreamble_InjectsSkillCatalog verifies that when Options.Skills is
// provided, the agent prompt names every available skill by Name plus its
// Description. This is what lets the LLM answer "what skills do you have?"
// with real names rather than guesses.
func TestSystemPreamble_InjectsSkillCatalog(t *testing.T) {
	prov := &capturingProvider{}
	reg := tools.New()
	skillReg := skills.New([]*skills.Skill{
		{
			Name:         "fake-skill-one",
			Description:  "first fake skill description",
			AllowedTools: []string{"x.y"},
			SystemPrompt: "irrelevant",
		},
		{
			Name:         "fake-skill-two",
			Description:  "second fake skill description",
			AllowedTools: []string{"x.y"},
			SystemPrompt: "irrelevant",
		},
	})
	ag, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
		Skills:   skillReg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag.Run(context.Background(), "hi", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := systemMessage(prov.captured)
	want := []string{
		"## Available skills",
		"fake-skill-one",
		"first fake skill description",
		"fake-skill-two",
		"second fake skill description",
	}
	for _, w := range want {
		if !strings.Contains(sys, w) {
			t.Errorf("system prompt missing skill-catalog phrase %q\n--- prompt ---\n%s\n--- end ---", w, sys)
		}
	}
}

// TestSystemPreamble_DropsCatalogWhenSkillActive pins the token-economy
// rule: once a skill is active its full body is injected, so the catalog of
// every OTHER skill is redundant noise and must be omitted. The catalog
// only appears in the no-active-skill case (covered above).
func TestSystemPreamble_DropsCatalogWhenSkillActive(t *testing.T) {
	prov := &capturingProvider{}
	reg := tools.New()
	skillReg := skills.New([]*skills.Skill{
		{
			Name:         "fake-skill-one",
			Description:  "first fake skill description",
			AllowedTools: []string{"x.y"},
			SystemPrompt: "irrelevant",
		},
	})
	active := skills.NewStaticSkill(&skills.Skill{
		Name:         "active-skill",
		Description:  "the active one",
		AllowedTools: []string{"x.y"},
		SystemPrompt: "ACTIVE-SKILL-BODY-MARKER",
	})
	ag, err := agent.New(agent.Options{
		Provider: prov,
		Model:    "test-model",
		Registry: reg,
		Skills:   skillReg,
		Skill:    active,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag.Run(context.Background(), "hi", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := systemMessage(prov.captured)
	// Assert on catalog CONTENT (the registry skill's name), not the
	// "## Available skills" header — basePreamble's prose self-references
	// that header phrase, so the header is present even when the catalog
	// section is not emitted. The skill name only appears if the catalog
	// was actually written.
	if strings.Contains(sys, "fake-skill-one") {
		t.Errorf("skill catalog must be dropped when a skill is active\n--- prompt ---\n%s\n--- end ---", sys)
	}
	if !strings.Contains(sys, "## Active skill: active-skill") || !strings.Contains(sys, "ACTIVE-SKILL-BODY-MARKER") {
		t.Errorf("active skill body must still be injected\n--- prompt ---\n%s\n--- end ---", sys)
	}
}

// TestSystemPreamble_InjectsEnvironmentMemory verifies that durable
// cross-session memory passed via Options.EnvironmentMemory is injected under
// an "## Environment memory" heading, and that nothing is injected when memory
// is empty (a fresh install must not add an empty section).
func TestSystemPreamble_InjectsEnvironmentMemory(t *testing.T) {
	const fact = "- (2026-06-01) ctx prod-east is production"

	prov := &capturingProvider{}
	ag, err := agent.New(agent.Options{
		Provider:          prov,
		Model:             "test-model",
		Registry:          tools.New(),
		EnvironmentMemory: fact,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag.Run(context.Background(), "hi", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// injectionMarker is unique to the dynamic memory block in buildSystemPrompt.
	// basePreamble's "## Cross-session memory" prose self-references the
	// "## Environment memory" heading, so asserting on the heading would be a
	// false positive — assert on this sentence instead (mirrors the skill-
	// catalog test's header-vs-content distinction).
	const injectionMarker = "re-verify with tools when a fact may"

	sys := systemMessage(prov.captured)
	if !strings.Contains(sys, injectionMarker) || !strings.Contains(sys, fact) {
		t.Errorf("recorded memory must be injected\n--- prompt ---\n%s\n--- end ---", sys)
	}

	// Empty memory → no injected block.
	prov2 := &capturingProvider{}
	ag2, err := agent.New(agent.Options{Provider: prov2, Model: "test-model", Registry: tools.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag2.Run(context.Background(), "hi", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(systemMessage(prov2.captured), injectionMarker) {
		t.Error("empty memory must not inject an Environment memory block")
	}
}

// TestRun_DoesNotReplayStaleSystemFromHistory pins the fix for the stale
// system-prompt bug: history can carry a system message from a prior turn (the
// agent returns one at msgs[0]), and some adapters resolve the system block
// last-wins, so an accumulated older copy would override the freshly built one.
// Run must skip RoleSystem messages when replaying history, so exactly one
// system message — the current one, carrying the latest memory — reaches the
// provider.
func TestRun_DoesNotReplayStaleSystemFromHistory(t *testing.T) {
	prov := &capturingProvider{}
	ag, err := agent.New(agent.Options{
		Provider:          prov,
		Model:             "test-model",
		Registry:          tools.New(),
		EnvironmentMemory: "FRESH-MEMORY-MARKER",
		History: []llm.Message{
			{Role: llm.RoleSystem, Content: "STALE-SYSTEM-MARKER"},
			{Role: llm.RoleUser, Content: "earlier question"},
			{Role: llm.RoleAssistant, Content: "earlier answer"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := ag.Run(context.Background(), "now", render.NewStream(discardWriter{}, render.NewTheme(true))); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sysCount := 0
	for _, m := range prov.captured.Messages {
		if m.Role == llm.RoleSystem {
			sysCount++
		}
	}
	if sysCount != 1 {
		t.Errorf("expected exactly one system message, got %d", sysCount)
	}
	sys := systemMessage(prov.captured)
	if !strings.Contains(sys, "FRESH-MEMORY-MARKER") {
		t.Errorf("fresh system prompt (with current memory) must be the one sent\n%s", sys)
	}
	if strings.Contains(sys, "STALE-SYSTEM-MARKER") {
		t.Errorf("stale system prompt from history must not be replayed\n%s", sys)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
