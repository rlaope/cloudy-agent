package main

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
	"github.com/rlaope/cloudy/internal/wiring"
)

// runAsk implements `cloudy ask "<prompt>"` (and `cloudy -p "<prompt>"` and
// stdin pipe). It is a one-shot non-TUI flow: load config, resolve provider,
// build skill + tool registries, run the ReAct loop, write to stdout.
func runAsk(args []string, stdout, stderr io.Writer) error {
	opts, rest, err := parseFlags("ask", args, stderr)
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
		return errf("ask requires a prompt: cloudy ask \"<prompt>\"  or  echo … | cloudy ask")
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

	toolReg, warn := wiring.BuildRegistry(wiring.Options{
		KubeconfigPath: opts.kubeconfig,
		ContextName:    opts.context,
		PromEndpoints:  cfg.Prometheus,
		EnableJVM:      true,
		EnablePython:   true,
		EnableGPU:      true,
	})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}

	// Apply the active Permission Profile (if any) before skill filtering,
	// so skills cannot widen what the profile permits.
	if p, err := permission.LoadActive(); err == nil {
		toolReg = permission.FilterRegistry(toolReg, p)
		fmt.Fprintf(stderr, "cloudy: profile=%s\n", p.Name)
	}

	var activeSkill *skillType
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
	defer sess.Close()

	theme := render.NewTheme(opts.noColor)
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

	ctx := context.Background()
	if _, err := ag.Run(ctx, prompt, stream); err != nil {
		return errf("run: %w", err)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "— model=%s session=%s\n", modelID, sess.ID)
	return nil
}
