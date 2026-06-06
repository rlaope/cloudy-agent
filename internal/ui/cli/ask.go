package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/runner"
	"github.com/rlaope/cloudy/internal/render"
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
	resume string
	plan   bool
}

func (o *askOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.StringVar(&o.model, "model", "", "model id (e.g. claude-haiku-4-5-20251001, gpt-4o)")
	fs.StringVar(&o.skill, "skill", "", "skill to activate (e.g. jvm-gc)")
	fs.StringVar(&o.prompt, "prompt", "", "prompt for one-shot mode (alias for positional)")
	fs.StringVar(&o.prompt, "p", "", "shorthand for --prompt")
	fs.StringVar(&o.resume, "resume", "", "resume a past session by id (continues its conversation)")
	fs.BoolVar(&o.plan, "plan", false, "plan-first: open multi-step investigations with a hypothesis plan before probing (off here; on by default in the TUI)")
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

	theme := render.NewTheme(opts.base.noColor)
	stream := render.NewStream(stdout, theme)

	result, err := runner.Run(ctx, runner.Request{
		Prompt:           prompt,
		ModelOverride:    opts.model,
		SkillName:        opts.skill,
		ResumeID:         opts.resume,
		KubeconfigPath:   opts.base.kubeconfig,
		ContextName:      opts.base.context,
		Plan:             opts.plan,
		Sink:             stream,
		Stderr:           stderr,
		Approver:         agent.DenyApprover(),
		UseActiveProfile: true,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "— model=%s session=%s\n", result.Model, result.SessionID)
	return nil
}
