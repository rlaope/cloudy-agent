package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/setup"
)

// runDoctor implements `cloudy doctor [--json]`. It runs the structured
// readiness checks from internal/setup, plus environment checks relevant
// to T2 (Bastion) deployments — the resolved cloudy home and any HTTP
// proxy configuration.
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
	checks = append(checks, environmentChecks()...)

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

// environmentChecks reports CLOUDY_HOME / HTTP proxy state. These are
// always informational (OK=true) — they exist so bastion / restricted
// network operators can confirm cloudy will egress through the right
// proxy and that per-user state lives where they expect.
func environmentChecks() []setup.Check {
	homeDetail := "default (~/.cloudy)"
	if v := os.Getenv("CLOUDY_HOME"); v != "" {
		homeDetail = v
	} else if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		homeDetail = filepath.Join(v, "cloudy")
	}

	proxyDetail := "no proxy configured"
	if v := firstNonEmpty(os.Getenv("HTTPS_PROXY"), os.Getenv("https_proxy")); v != "" {
		proxyDetail = "HTTPS_PROXY=" + v
	} else if v := firstNonEmpty(os.Getenv("HTTP_PROXY"), os.Getenv("http_proxy")); v != "" {
		proxyDetail = "HTTP_PROXY=" + v
	}
	if np := firstNonEmpty(os.Getenv("NO_PROXY"), os.Getenv("no_proxy")); np != "" {
		proxyDetail += "  NO_PROXY=" + np
	}

	return []setup.Check{
		{Name: "cloudy home", OK: true, Detail: homeDetail},
		{Name: "egress proxy", OK: true, Detail: proxyDetail},
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
