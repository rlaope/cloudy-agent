package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/wiring"
)

func init() { Register(&skillsCmd{}) }

type skillsCmd struct{}

func (skillsCmd) Name() string  { return "skills" }
func (skillsCmd) Short() string { return `list / show installed skills` }

type skillsOptions struct {
	base baseFlags
}

func (o *skillsOptions) bind(fs *flagSet) { o.base.bind(fs.FlagSet) }

func (skillsCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	// No subcommand → default to `list` so `cloudy skills` Just Works.
	// The full usage is still discoverable via `cloudy skills --help` and
	// the unknown-subcommand branch below.
	sub := "list"
	var rest []string
	if len(args) > 0 {
		if subIdx := findSkillsSubcommand(args); subIdx >= 0 {
			sub = args[subIdx]
			rest = append([]string{}, args[subIdx+1:]...)
			rest = append(rest, args[:subIdx]...)
		} else if strings.HasPrefix(args[0], "-") {
			rest = args
		} else {
			sub = args[0]
			rest = args[1:]
		}
	}

	reg, err := wiring.BuildSkillRegistry()
	if err != nil {
		return errf("skills: %w", err)
	}

	switch sub {
	case "list":
		var opts skillsOptions
		pos, err := parseInto(&opts, "skills list", rest, stderr)
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return errf("unexpected skills list argument: %s", pos[0])
		}
		if opts.base.asJSON {
			rows := make([]skillJSON, 0, len(reg.List()))
			for _, s := range reg.List() {
				rows = append(rows, skillToJSON(s))
			}
			return json.NewEncoder(stdout).Encode(rows)
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
		var opts skillsOptions
		pos, err := parseInto(&opts, "skills show", rest[1:], stderr)
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return errf("unexpected skills show argument: %s", pos[0])
		}
		if opts.base.asJSON {
			return json.NewEncoder(stdout).Encode(skillToJSON(s))
		}
		theme := render.NewTheme(opts.base.noColor)
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

func findSkillsSubcommand(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--context" || arg == "--kubeconfig" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--context=") || strings.HasPrefix(arg, "--kubeconfig=") {
			continue
		}
		switch arg {
		case "list", "show":
			return i
		}
	}
	return -1
}

type skillJSON struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Triggers        []string `json:"triggers,omitempty"`
	AllowedTools    []string `json:"allowed_tools,omitempty"`
	ModelPreference []string `json:"model_preference,omitempty"`
	Examples        []string `json:"examples,omitempty"`
	Requires        []string `json:"requires,omitempty"`
	SourcePath      string   `json:"source_path,omitempty"`
}

func skillToJSON(s *skills.Skill) skillJSON {
	return skillJSON{
		Name:            s.Name,
		Description:     s.Description,
		Triggers:        s.Triggers,
		AllowedTools:    s.AllowedTools,
		ModelPreference: s.ModelPreference,
		Examples:        s.Examples,
		Requires:        s.Requires,
		SourcePath:      s.SourcePath,
	}
}

func buildSkillMarkdown(s *skills.Skill) string {
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
