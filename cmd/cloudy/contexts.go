package main

import (
	"fmt"
	"io"

	"k8s.io/client-go/tools/clientcmd"
)

// runContexts implements `cloudy contexts` — list kubeconfig contexts.
func runContexts(args []string, stdout, stderr io.Writer) error {
	opts, _, err := parseFlags("contexts", args, stderr)
	if err != nil {
		return err
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.kubeconfig != "" {
		rules.ExplicitPath = opts.kubeconfig
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
