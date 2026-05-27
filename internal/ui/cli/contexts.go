package cli

import (
	"context"
	"fmt"
	"io"

	"k8s.io/client-go/tools/clientcmd"
)

func init() { Register(&contextsCmd{}) }

type contextsCmd struct{}

func (contextsCmd) Name() string  { return "contexts" }
func (contextsCmd) Short() string { return `list kubeconfig contexts` }

type contextsOptions struct {
	base baseFlags
}

func (o *contextsOptions) bind(fs *flagSet) { o.base.bind(fs.FlagSet) }

func (contextsCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	var opts contextsOptions
	if _, err := parseInto(&opts, "contexts", args, stderr); err != nil {
		return err
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.base.kubeconfig != "" {
		rules.ExplicitPath = opts.base.kubeconfig
	}
	cfg, err := rules.Load()
	if err != nil {
		return errf("kubeconfig: %w", err)
	}
	current := cfg.CurrentContext
	fmt.Fprintf(stdout, "%-3s  %-30s  %-30s  %s\n", "*", "NAME", "CLUSTER", "USER")
	for name, ctx := range cfg.Contexts {
		mark := " "
		if name == current {
			mark = "*"
		}
		fmt.Fprintf(stdout, "%-3s  %-30s  %-30s  %s\n", mark, name, ctx.Cluster, ctx.AuthInfo)
	}
	return nil
}
