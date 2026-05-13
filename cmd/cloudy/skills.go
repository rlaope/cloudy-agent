package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/wiring"
)

// skillType is a local alias so other files in this package can refer to
// *skillType without re-importing the skills package.
type skillType = skills.Skill

// runSkills implements `cloudy skills <list|show>`.
func runSkills(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy skills <list|show> [name]")
	}
	sub := args[0]
	rest := args[1:]

	reg, err := wiring.BuildSkillRegistry()
	if err != nil {
		return errf("skills: %w", err)
	}

	switch sub {
	case "list":
		_, _, err := parseFlags("skills list", rest, stderr)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%-22s  %-6s  %s\n", "NAME", "TOOLS", "DESCRIPTION")
		for _, s := range reg.List() {
			desc := s.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Fprintf(stdout, "%-22s  %-6d  %s\n", s.Name, len(s.AllowedTools), desc)
		}
		return nil

	case "show":
		if len(rest) < 1 {
			return errf("usage: cloudy skills show <name>")
		}
		s, ok := reg.Get(rest[0])
		if !ok {
			return errf("unknown skill: %s", rest[0])
		}
		opts, _, _ := parseFlags("skills show", rest[1:], stderr)
		theme := render.NewTheme(opts.noColor)
		md := buildSkillMarkdown(s)
		out, err := render.RenderMarkdown(md, theme, 80)
		if err != nil {
			fmt.Fprint(stdout, md)
			return nil
		}
		fmt.Fprint(stdout, out)
		return nil

	default:
		return errf("unknown skills subcommand: %s", sub)
	}
}

func buildSkillMarkdown(s *skillType) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", s.Name, s.Description)
	if len(s.Triggers) > 0 {
		fmt.Fprintf(&b, "**Triggers:** %s\n\n", strings.Join(s.Triggers, ", "))
	}
	if len(s.AllowedTools) > 0 {
		fmt.Fprintf(&b, "**Allowed tools:** %s\n\n", strings.Join(s.AllowedTools, ", "))
	}
	if len(s.ModelPreference) > 0 {
		fmt.Fprintf(&b, "**Model preference:** %s\n\n", strings.Join(s.ModelPreference, ", "))
	}
	if len(s.Examples) > 0 {
		fmt.Fprintln(&b, "**Examples:**")
		for _, e := range s.Examples {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b, "## System prompt")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, s.SystemPrompt)
	return b.String()
}
