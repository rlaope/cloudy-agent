// Package runner owns presentation-neutral Cloudy agent invocation wiring.
package runner

import (
	"context"
	"fmt"
	"io"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/memory"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/session"
	"github.com/rlaope/cloudy/internal/wiring"
)

// Request describes one Cloudy agent run, independent of the CLI, TUI, or
// ChatOps presentation surface.
type Request struct {
	Prompt           string
	ModelOverride    string
	SkillName        string
	ProfileName      string
	ResumeID         string
	KubeconfigPath   string
	ContextName      string
	Plan             bool
	Sink             render.Sink
	Stderr           io.Writer
	Approver         agent.Approver
	ConfigPath       string
	DefaultMasking   bool
	UseActiveProfile bool
}

// Result is the durable metadata produced by one runner invocation.
type Result struct {
	Model      string
	SessionID  string
	NewHistory []llm.Message
	// Masker is the same redaction policy used for disk-safe runner outputs.
	Masker *permission.Masker
}

// Run builds provider, tools, skills, profile, session, memory, and the
// agent.Options for one request, then executes the prompt.
func Run(ctx context.Context, req Request) (Result, error) {
	stderr := req.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	cfgPath := req.ConfigPath
	if cfgPath == "" {
		cfgPath = config.Path()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return Result{}, fmt.Errorf("config: %w", err)
	}
	model := req.ModelOverride
	if model == "" {
		model = cfg.DefaultModel
	}
	if model == "" {
		return Result{}, fmt.Errorf("no model set; use --model or run `cloudy setup`")
	}

	provider, modelID, err := wiring.BuildProvider(model)
	if err != nil {
		return Result{}, err
	}

	skillReg, err := wiring.BuildSkillRegistry()
	if err != nil {
		return Result{}, fmt.Errorf("skills: %w", err)
	}

	activeProfile, err := loadProfile(req.ProfileName, req.UseActiveProfile)
	if err == nil && activeProfile != nil {
		fmt.Fprintf(stderr, "cloudy: profile=%s\n", activeProfile.Name)
	} else if err != nil && req.ProfileName != "" {
		return Result{}, err
	}
	maskProfile := activeProfile
	if req.DefaultMasking && !hasMasking(maskProfile) {
		maskProfile = &permission.Profile{Masking: permission.DefaultMaskingPatterns()}
	}
	masker := permission.MaskerOrDefault(maskProfile)

	toolReg, warn := wiring.Rebuild(cfg, wiring.RebuildOpts{
		KubeconfigPath:   req.KubeconfigPath,
		ContextName:      req.ContextName,
		Profile:          activeProfile,
		UseActiveProfile: req.UseActiveProfile,
	})
	if warn != nil {
		fmt.Fprintf(stderr, "cloudy: %v\n", warn)
	}

	var activeSkill skills.SkillProvider
	if req.SkillName != "" {
		s, ok := skillReg.Get(req.SkillName)
		if !ok {
			return Result{}, fmt.Errorf("unknown skill: %s", req.SkillName)
		}
		activeSkill = skills.NewStaticSkill(s)
		toolReg = toolReg.Filter(s.AllowedTools)
	}

	var history []llm.Message
	resumeID := ""
	if req.ResumeID != "" {
		h, _, lerr := session.LoadHistory(req.ResumeID)
		if lerr != nil {
			return Result{}, fmt.Errorf("resume: %w", lerr)
		}
		history = h
		resumeID = req.ResumeID
	}

	sess, err := session.New(resumeID)
	if err != nil {
		return Result{}, fmt.Errorf("session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	ag, err := agent.New(agent.Options{
		Provider:                 provider,
		Model:                    modelID,
		Registry:                 toolReg,
		Skill:                    activeSkill,
		Skills:                   skillReg,
		MaxTokensPerSession:      cfg.Safety.MaxTokensPerSession,
		MaxUSDPerDay:             cfg.Safety.MaxUSDPerDay,
		MaxConversationSeconds:   cfg.Safety.MaxConversationSeconds,
		MaxLogLinesPerCall:       permission.EffectiveLogLines(activeProfile, cfg.Safety.MaxLogLines),
		MaxProfileSecondsPerCall: permission.EffectiveProfileSeconds(activeProfile, cfg.Safety.MaxProfileSeconds),
		MaxLogResponseBytes:      cfg.Safety.MaxLogResponseBytes,
		Approver:                 req.Approver,
		Profile:                  maskProfile,
		History:                  history,
		Plan:                     req.Plan,
		EnvironmentMemory:        loadEnvMemory(stderr),
		SimilarIncidentCases:     loadSimilarIncidentCases(req.Prompt, activeProfile, stderr),
	})
	if err != nil {
		return Result{}, fmt.Errorf("agent: %w", err)
	}

	newMsgs, runErr := ag.Run(ctx, req.Prompt, req.Sink)
	if runErr != nil {
		return Result{}, fmt.Errorf("run: %w", runErr)
	}
	if len(newMsgs) > 0 {
		masked := permission.MaskHistory(activeProfile, newMsgs)
		if serr := session.SaveHistory(sess.ID, modelID, masked); serr != nil {
			fmt.Fprintf(stderr, "cloudy: resume save: %v\n", serr)
		}
	}
	return Result{Model: modelID, SessionID: sess.ID, NewHistory: newMsgs, Masker: masker}, nil
}

func loadProfile(name string, useActive bool) (*permission.Profile, error) {
	if name != "" {
		p, err := permission.Load(name)
		if err != nil {
			return nil, fmt.Errorf("profile: %w", err)
		}
		return p, nil
	}
	if !useActive {
		return nil, nil
	}
	p, err := permission.LoadActive()
	if err != nil {
		return nil, nil
	}
	return p, nil
}

func hasMasking(p *permission.Profile) bool {
	return p != nil && (len(p.Masking.KeyRegex) > 0 || len(p.Masking.ValueRegex) > 0)
}

func loadEnvMemory(stderr io.Writer) string {
	mem, err := memory.Load()
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: memory: %v\n", err)
		return ""
	}
	return mem
}

func loadSimilarIncidentCases(prompt string, profile *permission.Profile, stderr io.Writer) string {
	rendered, err := agent.BuildIncidentMemoryPrompt(prompt, profile)
	if err != nil {
		fmt.Fprintf(stderr, "cloudy: incident memory: %v\n", err)
		return ""
	}
	return rendered
}
