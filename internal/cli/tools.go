package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/wiring"
)

func init() { Register(&toolsCmd{}) }

type toolsCmd struct{}

func (toolsCmd) Name() string  { return "tools" }
func (toolsCmd) Short() string { return `list registered tool groups and skipped ones with reasons` }

type toolsOptions struct {
	base baseFlags
}

func (o *toolsOptions) bind(fs *flagSet) { o.base.bind(fs.FlagSet) }

func (toolsCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	var opts toolsOptions
	if _, err := parseInto(&opts, "tools", args, stderr); err != nil {
		return err
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		return errf("config: %w", err)
	}

	activeProfile, _ := permission.LoadActive()

	reg, warn := wiring.BuildRegistry(wiring.Options{
		KubeconfigPath: opts.base.kubeconfig,
		ContextName:    opts.base.context,
		Contexts:       cfg.Contexts,
		Profile:        activeProfile,
		PromEndpoints:  cfg.Prometheus,
		Databases:      cfg.Databases,
		Logs:           cfg.Logs,
		Tracing:        cfg.Tracing,
	})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}

	inv := reg.Inventory()

	if opts.base.asJSON {
		return json.NewEncoder(stdout).Encode(inv)
	}

	fmt.Fprintf(stdout, "%-10s  %-7s  %s\n", "GROUP", "STATUS", "DETAIL")
	for _, g := range inv.Groups {
		if g.Skipped {
			fmt.Fprintf(stdout, "%-10s  %-7s  %s\n", g.Name, "skipped", g.Reason)
			continue
		}
		fmt.Fprintf(stdout, "%-10s  %-7s  %s\n", g.Name, "ok", strings.Join(g.Tools, ", "))
	}
	return nil
}
