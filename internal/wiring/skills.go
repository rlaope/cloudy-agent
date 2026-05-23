package wiring

import (
	"path/filepath"

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
// exists in the supplied tool registry. Without this, a typo like
// k8s.get_pod (instead of k8s.describe_pod) silently breaks the skill until
// the LLM tries to invoke that tool mid-conversation.
func ValidateSkillToolRefs(reg *skills.Registry, tr *tools.Registry) error {
	if reg == nil || tr == nil {
		return nil
	}
	all := tr.List()
	known := make(map[string]struct{}, len(all))
	for _, t := range all {
		known[t.Name()] = struct{}{}
	}
	return reg.Validate(known)
}
