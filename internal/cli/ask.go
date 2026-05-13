package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/agent"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/wiring"
)

func init() { Register(&askCmd{}) }

type askCmd struct{}

func (askCmd) Name() string  { return "ask" }
func (askCmd) Short() string { return `one-shot natural-language query` }

type askOptions struct {
	base   baseFlags
	model  string
	skill  string
	prompt string
}

func (o *askOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.StringVar(&o.model, "model", "", "model id (e.g. claude-haiku-4-5-20251001, gpt-4o)")
	fs.StringVar(&o.skill, "skill", "", "skill to activate (e.g. jvm-gc)")
	fs.StringVar(&o.prompt, "prompt", "", "prompt for one-shot mode (alias for positional)")
	fs.StringVar(&o.prompt, "p", "", "shorthand for --prompt")
}

func (askCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var opts askOptions
	rest, err := parseInto(&opts, "ask", args, stderr)
	if err != nil {
		return err
	}

	prompt := opts.prompt
	if prompt == "" {
		prompt = strings.TrimSpace(strings.Join(rest, " "))
	}
	if prompt == "" && !isatty.IsTerminal(os.Stdin.Fd()) {
		b, _ := io.ReadAll(os.Stdin)
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		return errf(`ask requires a prompt: cloudy ask "<prompt>"  or  echo … | cloudy ask`)
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		return errf("config: %w", err)
	}
	model := opts.model
	if model == "" {
		model = cfg.DefaultModel
	}
	if model == "" {
		return errf("no model set; use --model or run `cloudy setup`")
	}

	provider, modelID, err := wiring.BuildProvider(model)
	if err != nil {
		return err
	}

	skillReg, err := wiring.BuildSkillRegistry()
	if err != nil {
		return errf("skills: %w", err)
	}

	activeProfile, _ := permission.LoadActive()

	toolReg, warn := wiring.BuildRegistry(wiring.Options{
		KubeconfigPath: opts.base.kubeconfig,
		ContextName:    opts.base.context,
		Contexts:       cfg.Contexts,
		Profile:        activeProfile,
		PromEndpoints:  cfg.Prometheus,
	})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}
	if activeProfile != nil {
		fmt.Fprintf(stderr, "cloudy: profile=%s\n", activeProfile.Name)
	}

	var activeSkill *skills.Skill
	if opts.skill != "" {
		s, ok := skillReg.Get(opts.skill)
		if !ok {
			return errf("unknown skill: %s", opts.skill)
		}
		activeSkill = s
		toolReg = toolReg.Filter(s.AllowedTools)
	}

	sess, err := session.New("")
	if err != nil {
		return errf("session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	theme := render.NewTheme(opts.base.noColor)
	stream := render.NewStream(stdout, theme)

	ag, err := agent.New(agent.Options{
		Provider: provider,
		Model:    modelID,
		Registry: toolReg,
		Skill:    activeSkill,
	})
	if err != nil {
		return errf("agent: %w", err)
	}

	if _, err := ag.Run(ctx, prompt, stream); err != nil {
		return errf("run: %w", err)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "— model=%s session=%s\n", modelID, sess.ID)
	return nil
}
