package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
)

func init() { Register(&profileCmd{}) }

type profileCmd struct{}

func (profileCmd) Name() string  { return "profile" }
func (profileCmd) Short() string { return `manage permission profiles` }

func (profileCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy profile <list|show|use|new|none|cluster>")
	}
	_ = stderr

	switch args[0] {
	case "list":
		return profileList(stdout)
	case "show":
		if len(args) < 2 {
			return errf("usage: cloudy profile show <name>")
		}
		return profileShow(stdout, args[1])
	case "use":
		if len(args) < 2 {
			return errf("usage: cloudy profile use <name>")
		}
		if err := permission.SetActive(args[1]); err != nil {
			return errf("profile use: %w", err)
		}
		fmt.Fprintf(stdout, "active profile set to %s\n", args[1])
		return nil
	case "none":
		if err := permission.ClearActive(); err != nil {
			return errf("profile none: %w", err)
		}
		fmt.Fprintln(stdout, "active profile cleared")
		return nil
	case "new":
		if len(args) < 2 {
			return errf("usage: cloudy profile new <name>")
		}
		return profileNew(stdout, args[1])
	case "cluster":
		return profileCluster(stdout)
	default:
		return errf("unknown profile subcommand: %s", args[0])
	}
}

func profileList(stdout io.Writer) error {
	names, err := permission.List()
	if err != nil {
		return errf("profile list: %w", err)
	}
	active, _ := permission.Active()
	if len(names) == 0 {
		fmt.Fprintln(stdout, "no permission profiles yet — `cloudy profile new <name>` to create one")
		return nil
	}
	fmt.Fprintf(stdout, "%-3s  %-22s  %s\n", "*", "NAME", "DESCRIPTION")
	for _, n := range names {
		mark := " "
		if n == active {
			mark = "*"
		}
		desc := ""
		if p, err := permission.Load(n); err == nil {
			desc = p.Description
		}
		fmt.Fprintf(stdout, "%-3s  %-22s  %s\n", mark, n, desc)
	}
	return nil
}

func profileShow(stdout io.Writer, name string) error {
	p, err := permission.Load(name)
	if err != nil {
		return errf("profile show: %w", err)
	}
	fmt.Fprintf(stdout, "name: %s\n", p.Name)
	if p.Description != "" {
		fmt.Fprintf(stdout, "description: %s\n", p.Description)
	}
	if len(p.Contexts) > 0 {
		fmt.Fprintf(stdout, "contexts: %v\n", p.Contexts)
	}
	if len(p.Tools.Allow)+len(p.Tools.Deny) > 0 {
		fmt.Fprintf(stdout, "tools.allow: %v\n", p.Tools.Allow)
		fmt.Fprintf(stdout, "tools.deny:  %v\n", p.Tools.Deny)
	}
	if len(p.Namespaces.Allow)+len(p.Namespaces.Deny) > 0 {
		fmt.Fprintf(stdout, "namespaces.allow: %v\n", p.Namespaces.Allow)
		fmt.Fprintf(stdout, "namespaces.deny:  %v\n", p.Namespaces.Deny)
	}
	if p.Limits.MaxLogLines+p.Limits.MaxProfileSeconds+p.Limits.MaxTokensPerSession > 0 ||
		p.Limits.MaxUSDPerDay > 0 {
		fmt.Fprintf(stdout, "limits.max_log_lines: %d\n", p.Limits.MaxLogLines)
		fmt.Fprintf(stdout, "limits.max_profile_seconds: %d\n", p.Limits.MaxProfileSeconds)
		fmt.Fprintf(stdout, "limits.max_tokens_per_session: %d\n", p.Limits.MaxTokensPerSession)
		fmt.Fprintf(stdout, "limits.max_usd_per_day: %.2f\n", p.Limits.MaxUSDPerDay)
	}
	return nil
}

func profileNew(stdout io.Writer, name string) error {
	if _, err := permission.Load(name); err == nil {
		return errf("profile new: %s already exists", name)
	} else if !errors.Is(err, permission.ErrNotFound) {
		return errf("profile new: %w", err)
	}
	p := &permission.Profile{
		Name:        name,
		Description: "starter profile — narrow as needed",
		Tools: permission.Tools{
			Allow: []string{"k8s.*", "prom.*"},
			Deny:  []string{"jvm.async_profile"},
		},
		Limits: permission.Limits{
			MaxLogLines:         2000,
			MaxTokensPerSession: 200000,
		},
	}
	if err := permission.Save(p); err != nil {
		return errf("profile new: %w", err)
	}
	fmt.Fprintf(stdout, "created %s\n", permission.Path(name))
	fmt.Fprintf(stdout, "activate with: cloudy profile use %s\n", name)
	return nil
}

func profileCluster(stdout io.Writer) error {
	p, err := config.LoadProfile(config.ProfilePath())
	if err != nil {
		return errf("cluster profile: %w", err)
	}
	if !p.IsValid() {
		fmt.Fprintln(stdout, "no cluster profile yet — run `cloudy setup`.")
		return nil
	}
	fmt.Fprintf(stdout, "schema=%d  generated=%s  contexts=%d  recommended_skills=%d\n",
		p.SchemaVersion, p.GeneratedAt.Format("2006-01-02 15:04:05"),
		len(p.Contexts), len(p.RecommendedSkills))
	for _, c := range p.Contexts {
		fmt.Fprintf(stdout, "  - %s  reachable=%v  nodes=%d  gpu=%d  prometheus=%v  pod_sample=%s  runtimes=%s  frontend_pods=%d  ingress_hosts=%d\n",
			c.Name, c.Reachable, c.NodeCount, c.GPUNodeCount, c.HasPrometheus, formatPodSample(c), formatRuntimePodCounts(c), c.FrontendPodCount, c.IngressHostCount)
	}
	return nil
}

func formatPodSample(c config.ContextProfile) string {
	if !c.PodSampleScanned && c.PodSampleCount == 0 && !c.PodSampleIncomplete {
		return "-"
	}
	if c.PodSampleIncomplete {
		if c.PodSampleIncompleteReason != "" {
			return fmt.Sprintf("%d(incomplete:%s)", c.PodSampleCount, c.PodSampleIncompleteReason)
		}
		return fmt.Sprintf("%d(incomplete)", c.PodSampleCount)
	}
	return fmt.Sprintf("%d", c.PodSampleCount)
}

func formatRuntimePodCounts(c config.ContextProfile) string {
	counts := make(map[string]int, len(c.RuntimePodCounts)+2)
	for runtime, count := range c.RuntimePodCounts {
		if count > 0 {
			counts[runtime] = count
		}
	}
	if _, ok := counts["jvm"]; !ok && c.JVMPodCount > 0 {
		counts["jvm"] = c.JVMPodCount
	}
	if _, ok := counts["python"]; !ok && c.PythonPodCount > 0 {
		counts["python"] = c.PythonPodCount
	}
	if len(counts) == 0 {
		return "-"
	}

	keys := make([]string, 0, len(counts))
	for runtime := range counts {
		keys = append(keys, runtime)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, runtime := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", runtime, counts[runtime]))
	}
	return strings.Join(parts, ",")
}
