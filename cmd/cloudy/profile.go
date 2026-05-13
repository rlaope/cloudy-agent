package main

import (
	"fmt"
	"io"

	"github.com/rlaope/cloudy/internal/config"
)

// runProfile implements `cloudy profile <list|show>`. v0.1 only supports
// the read paths; `use` and `new` are deferred to the Permission Profiles
// milestone.
func runProfile(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy profile <list|show>")
	}
	_ = stderr
	switch args[0] {
	case "list":
		p, err := config.LoadProfile(config.ProfilePath())
		if err != nil {
			return errf("profile: %w", err)
		}
		if !p.IsValid() {
			fmt.Fprintln(stdout, "no profile yet — run `cloudy setup`.")
			return nil
		}
		fmt.Fprintf(stdout, "schema=%d  generated=%s  contexts=%d  recommended_skills=%d\n",
			p.SchemaVersion, p.GeneratedAt.Format("2006-01-02 15:04:05"),
			len(p.Contexts), len(p.RecommendedSkills))
		for _, c := range p.Contexts {
			fmt.Fprintf(stdout, "  - %s  reachable=%v  nodes=%d  gpu=%d  jvm_pods=%d  py_pods=%d\n",
				c.Name, c.Reachable, c.NodeCount, c.GPUNodeCount, c.JVMPodCount, c.PythonPodCount)
		}
		return nil
	case "show":
		// Same as list for v0.1.
		return runProfile([]string{"list"}, stdout, stderr)
	case "use", "new":
		return errf("`cloudy profile %s` is not implemented in v0.1 (Permission Profiles arrive in v0.2)", args[0])
	default:
		return errf("unknown profile subcommand: %s", args[0])
	}
}
