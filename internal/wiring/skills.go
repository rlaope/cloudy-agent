package wiring

import (
	"path/filepath"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/skills"
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
