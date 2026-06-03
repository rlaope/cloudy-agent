package cli

import (
	"context"
	"io"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/setup"
)

func init() { Register(&setupCmd{}) }

type setupCmd struct{}

func (setupCmd) Name() string  { return "setup" }
func (setupCmd) Short() string { return `discover clusters, write ~/.cloudy/{config,profile}.yaml` }

type setupOptions struct {
	base   baseFlags
	auto   bool
	dryRun bool
}

func (o *setupOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.BoolVar(&o.auto, "auto", false, "skip interactive prompts")
	fs.BoolVar(&o.dryRun, "dry-run", false, "do not write files")
}

func (setupCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var opts setupOptions
	if _, err := parseInto(&opts, "setup", args, stderr); err != nil {
		return err
	}
	_ = stdout
	theme := render.NewTheme(opts.base.noColor)
	return setup.Run(ctx, setup.WizardOptions{
		Theme:          theme,
		ConfigPath:     config.Path(),
		ProfilePath:    config.ProfilePath(),
		KubeconfigPath: opts.base.kubeconfig,
		AutoRun:        opts.auto,
		DryRun:         opts.dryRun,
	})
}
