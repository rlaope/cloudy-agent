package wiring

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
)

// BuildSkillRegistry loads built-in skills and merges any user skills found
// under the "skills/" directory sibling to config.Path().
func BuildSkillRegistry() (*skills.Registry, error) {
	builtin, err := skills.LoadBuiltin()
	if err != nil {
		return nil, err
	}

	// User skills live next to config.yaml, e.g. ~/.cloudy/skills/.
	userDir := filepath.Join(filepath.Dir(config.Path()), "skills")
	user, err := skills.LoadUser(userDir)
	if err != nil {
		// Non-fatal: return built-ins only on user-dir errors.
		return skills.New(builtin), nil
	}

	merged := skills.Merge(builtin, user)
	return skills.New(merged), nil
}

// ValidateSkillToolRefs checks that every tool name referenced by any skill
// exists in the supplied tool registry, but suppresses warnings for tools
// that belong to a SKIPPED group (e.g. `log.loki_query_range` when the
// `log` group was skipped because no Loki endpoint is configured). The
// previous behaviour dumped a wall of ~30 lines on every startup against
// any environment that didn't have Loki/Tempo/Argo/etc. wired — built-in
// skills like `crashloop-deep-dive` and `incident-context` legitimately
// reference those tools as part of their workflow.
//
// Typos in user skills (e.g. `k8s.get_pod` instead of `k8s.describe_pod`)
// still surface because the `k8s` group is wired, so its tool name set is
// fully known and any mismatch is a real error.
func ValidateSkillToolRefs(reg *skills.Registry, tr *tools.Registry) error {
	if reg == nil || tr == nil {
		return nil
	}
	all := tr.List()
	known := make(map[string]struct{}, len(all))
	for _, t := range all {
		known[t.Name()] = struct{}{}
	}
	skipped := tr.Skipped()

	var errs []string
	for _, s := range reg.List() {
		for _, tool := range s.AllowedTools {
			if _, ok := known[tool]; ok {
				continue
			}
			// Suppress refs to tools in a skipped group — the operator
			// already saw the skipped-group banner on startup and can't
			// act on the skill complaining about it.
			group := groupPrefix(tool)
			if _, isSkipped := skipped[group]; isSkipped {
				continue
			}
			errs = append(errs, fmt.Sprintf("skill %q references unknown tool %q", s.Name, tool))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("skills: validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// groupPrefix returns the segment before the first '.' in a tool name —
// "log.loki_query_range" → "log". A bare name with no '.' is its own group.
// Mirrors internal/tools.groupOf without exporting an extra symbol just for
// this caller.
func groupPrefix(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}
