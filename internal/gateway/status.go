// Package gateway owns operator-facing setup and status reporting for ChatOps
// gateway connectors.
package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
)

const (
	PlatformSlack    = "slack"
	PlatformDiscord  = "discord"
	PlatformTelegram = "telegram"
)

// Requirement describes one field or secret a platform needs before it can run.
type Requirement struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
	Detail   string `json:"detail,omitempty"`
}

// PlatformReport is the readiness summary for one ChatOps connector.
type PlatformReport struct {
	Platform     string        `json:"platform"`
	Enabled      bool          `json:"enabled"`
	Mode         string        `json:"mode,omitempty"`
	Ready        bool          `json:"ready"`
	Requirements []Requirement `json:"requirements"`
	Endpoints    []string      `json:"endpoints,omitempty"`
	Notes        []string      `json:"notes,omitempty"`
}

// Report summarizes the ChatOps gateway configuration.
type Report struct {
	Enabled           bool             `json:"enabled"`
	Ready             bool             `json:"ready"`
	Listen            string           `json:"listen"`
	PublicURL         string           `json:"public_url,omitempty"`
	DefaultVisibility string           `json:"default_visibility"`
	SessionPath       string           `json:"session_path"`
	Platforms         []PlatformReport `json:"platforms"`
	Warnings          []string         `json:"warnings,omitempty"`
}

// Status computes a read-only gateway status from config and the process
// environment. It does not make network calls and never reveals secret values.
func Status(cfg config.Config) Report {
	rep := Report{
		Enabled:           cfg.ChatOps.Enabled,
		Listen:            firstNonEmpty(cfg.ChatOps.Listen, "127.0.0.1:8787"),
		PublicURL:         cfg.ChatOps.PublicURL,
		DefaultVisibility: firstNonEmpty(cfg.ChatOps.DefaultVisibility, "private"),
		SessionPath:       firstNonEmpty(cfg.ChatOps.Session.Path, defaultSessionMapPath()),
	}
	rep.Platforms = []PlatformReport{
		slackReport(cfg.ChatOps),
		discordReport(cfg.ChatOps),
		telegramReport(cfg.ChatOps),
	}
	anyEnabled := false
	allEnabledReady := true
	for _, p := range rep.Platforms {
		if !p.Enabled {
			continue
		}
		anyEnabled = true
		if !p.Ready {
			allEnabledReady = false
		}
	}
	rep.Ready = rep.Enabled && anyEnabled && allEnabledReady
	if !rep.Enabled {
		rep.Warnings = append(rep.Warnings, "chatops.enabled is false; run `cloudy gateway setup` before serving.")
	}
	if rep.Enabled && !anyEnabled {
		rep.Warnings = append(rep.Warnings, "no chat platform is enabled.")
	}
	return rep
}

func slackReport(cfg config.ChatOpsConfig) PlatformReport {
	slack := cfg.Platforms.Slack
	rep := PlatformReport{
		Platform: PlatformSlack,
		Enabled:  slack.Enabled,
		Mode:     firstNonEmpty(slack.Mode, "http"),
		Endpoints: []string{
			"/chatops/slack/commands",
			"/chatops/slack/events",
		},
	}
	rep.Requirements = []Requirement{
		envRequirement("signing_secret_env", "signing secret env value", slack.SigningSecretEnv, slack.Enabled, "verifies Slack request signatures"),
		envRequirement("bot_token_env", "bot token env value", slack.BotTokenEnv, slack.Enabled, "sends Slack event replies"),
		listRequirement("allowed_team_ids", "allowed workspace/team IDs", slack.AllowedTeamIDs, slack.Enabled, "at least one Slack workspace"),
		anyListRequirement("allowed_channel_or_user_ids", "allowed channel or user IDs", slack.Enabled, "at least one Slack channel or user", slack.AllowedChannelIDs, slack.AllowedUserIDs),
	}
	rep.Ready = slack.Enabled && ready(rep.Requirements)
	return rep
}

func discordReport(cfg config.ChatOpsConfig) PlatformReport {
	discord := cfg.Platforms.Discord
	rep := PlatformReport{
		Platform:  PlatformDiscord,
		Enabled:   discord.Enabled,
		Mode:      "interactions",
		Endpoints: []string{"/chatops/discord/interactions"},
		Notes: []string{
			"Discord interaction mode uses application_id and public_key; bot tokens are not required by this gateway mode.",
		},
	}
	rep.Requirements = []Requirement{
		valueRequirement("application_id", "application ID", discord.ApplicationID, discord.Enabled, "used for Discord followup webhook URLs"),
		envRequirement("public_key_env", "public key env value", discord.PublicKeyEnv, discord.Enabled, "verifies Discord interaction signatures"),
		listRequirement("allowed_guild_ids", "allowed guild IDs", discord.AllowedGuildIDs, discord.Enabled, "at least one Discord server"),
		anyListRequirement("allowed_channel_or_user_ids", "allowed channel or user IDs", discord.Enabled, "at least one Discord channel or user", discord.AllowedChannelIDs, discord.AllowedUserIDs),
	}
	rep.Ready = discord.Enabled && ready(rep.Requirements)
	return rep
}

func telegramReport(cfg config.ChatOpsConfig) PlatformReport {
	telegram := cfg.Platforms.Telegram
	mode := firstNonEmpty(telegram.Mode, "webhook")
	rep := PlatformReport{
		Platform: PlatformTelegram,
		Enabled:  telegram.Enabled,
		Mode:     mode,
		Endpoints: []string{
			"/chatops/telegram/webhook",
		},
	}
	rep.Requirements = []Requirement{
		envRequirement("bot_token_env", "bot token env value", telegram.BotTokenEnv, telegram.Enabled, "calls Telegram Bot API"),
		listRequirement("allowed_chat_ids", "allowed chat IDs", telegram.AllowedChatIDs, telegram.Enabled, "at least one Telegram chat"),
	}
	if mode != "polling" {
		rep.Requirements = append(rep.Requirements,
			envRequirement("webhook_secret_env", "webhook secret env value", telegram.WebhookSecretEnv, telegram.Enabled, "verifies Telegram webhook secret token"),
			valueRequirement("public_url", "public base URL", cfg.PublicURL, telegram.Enabled, "used by telegram-set-webhook"),
		)
	}
	rep.Ready = telegram.Enabled && ready(rep.Requirements)
	return rep
}

func envRequirement(key, label, envName string, required bool, detail string) Requirement {
	set := envName != "" && os.Getenv(envName) != ""
	if required && envName != "" && !set {
		detail = strings.TrimSpace(detail + fmt.Sprintf("; %s is not set", envName))
	}
	return Requirement{Key: key, Label: label, Required: required, Set: !required || set, Detail: detail}
}

func valueRequirement(key, label, value string, required bool, detail string) Requirement {
	set := strings.TrimSpace(value) != ""
	return Requirement{Key: key, Label: label, Required: required, Set: !required || set, Detail: detail}
}

func listRequirement(key, label string, values []string, required bool, detail string) Requirement {
	set := len(values) > 0
	return Requirement{Key: key, Label: label, Required: required, Set: !required || set, Detail: detail}
}

func anyListRequirement(key, label string, required bool, detail string, lists ...[]string) Requirement {
	set := false
	for _, values := range lists {
		if len(values) > 0 {
			set = true
			break
		}
	}
	return Requirement{Key: key, Label: label, Required: required, Set: !required || set, Detail: detail}
}

func ready(reqs []Requirement) bool {
	for _, r := range reqs {
		if r.Required && !r.Set {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultSessionMapPath() string {
	if ch := os.Getenv("CLOUDY_HOME"); ch != "" {
		return filepath.Join(ch, "chatops", "sessions.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cloudy", "chatops", "sessions.json")
	}
	return filepath.Join(".cloudy", "chatops", "sessions.json")
}
