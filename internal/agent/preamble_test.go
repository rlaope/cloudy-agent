package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
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

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
