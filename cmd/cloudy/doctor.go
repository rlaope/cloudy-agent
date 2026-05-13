package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/setup"
)

// runDoctor implements `cloudy doctor [--json]`. It runs the structured
// readiness checks from internal/setup and prints a checklist (or JSON).
func runDoctor(args []string, stdout, stderr io.Writer) error {
	opts, _, err := parseFlags("doctor", args, stderr)
	if err != nil {
		return err
	}
	checks, err := setup.Doctor(context.Background(), setup.Options{
		ConfigPath:  config.Path(),
		ProfilePath: config.ProfilePath(),
	})
	if err != nil {
		return errf("doctor: %w", err)
	}

	if opts.asJSON {
		return json.NewEncoder(stdout).Encode(checks)
	}
	allOK := true
	for _, c := range checks {
		mark := "✔"
		if !c.OK {
			mark = "✘"
			allOK = false
		}
		fmt.Fprintf(stdout, "%s  %-32s  %s\n", mark, c.Name, c.Detail)
	}
	if !allOK {
		fmt.Fprintln(stdout, "\nrun `cloudy setup` to fix the failing checks.")
	}
	return nil
}
