package cli

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

func init() { Register(&doctorCmd{}) }

type doctorCmd struct{}

func (doctorCmd) Name() string  { return "doctor" }
func (doctorCmd) Short() string { return `verify setup and reachability` }

type doctorOptions struct {
	base baseFlags
}

func (o *doctorOptions) bind(fs *flagSet) { o.base.bind(fs.FlagSet) }

func (doctorCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var opts doctorOptions
	if _, err := parseInto(&opts, "doctor", args, stderr); err != nil {
		return err
	}
	checks, err := setup.Doctor(ctx, setup.Options{
		ConfigPath:     config.Path(),
		KubeconfigPath: opts.base.kubeconfig,
		ProfilePath:    config.ProfilePath(),
	})
	if err != nil {
		return errf("doctor: %w", err)
	}
	checks = append(checks, environmentChecks()...)

	if opts.base.asJSON {
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
