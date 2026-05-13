package main

import (
	"context"
	"io"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/setup"
)

// runSetup implements `cloudy setup [--auto] [--rescan] [--dry-run]`.
// It launches the bubbletea wizard that discovers clusters and writes
// ~/.cloudy/{config,profile}.yaml.
func runSetup(args []string, stdout, stderr io.Writer) error {
	opts, _, err := parseFlags("setup", args, stderr)
	if err != nil {
		return err
	}
	_ = stdout
	theme := render.NewTheme(opts.noColor)
	return setup.Run(context.Background(), setup.WizardOptions{
		Theme:          theme,
		ConfigPath:     config.Path(),
		ProfilePath:    config.ProfilePath(),
		KubeconfigPath: opts.kubeconfig,
		AutoRun:        opts.auto,
	})
}
