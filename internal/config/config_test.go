package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
)

func TestDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.Safety.AllowSecrets {
		t.Error("Default: AllowSecrets should be false")
	}
	if cfg.Safety.MaxLogLines != 5000 {
		t.Errorf("Default: MaxLogLines = %d, want 5000", cfg.Safety.MaxLogLines)
	}
	if cfg.Safety.MaxProfileSeconds != 60 {
		t.Errorf("Default: MaxProfileSeconds = %d, want 60", cfg.Safety.MaxProfileSeconds)
	}
	// DefaultModel is intentionally empty — see the package comment on
	// Default(). The /login conversation owns model selection now;
	// hard-coding a specific id here was a deprecation-time-bomb.
	if cfg.DefaultModel != "" {
		t.Errorf("Default: DefaultModel must be empty (model picked via /login), got %q", cfg.DefaultModel)
	}
	if cfg.ChatOps.Enabled {
		t.Error("Default: ChatOps should be disabled")
	}
	if cfg.ChatOps.Listen != "127.0.0.1:8787" {
		t.Errorf("Default: ChatOps.Listen = %q, want loopback default", cfg.ChatOps.Listen)
	}
	if cfg.ChatOps.Platforms.Slack.SigningSecretEnv != "CLOUDY_SLACK_SIGNING_SECRET" {
		t.Errorf("Default: Slack signing secret env = %q", cfg.ChatOps.Platforms.Slack.SigningSecretEnv)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := config.Default()
	original.DefaultModel = "my-test-model"
	original.Safety.MaxLogLines = 1234
	original.Safety.AllowSecrets = true
	original.Routing.CheapToolStrongSummary = true
	original.ChatOps.Enabled = true
	original.ChatOps.PublicURL = "https://cloudy.example.test"
	original.ChatOps.MaxConcurrentRuns = 2
	original.ChatOps.Platforms.Slack.Enabled = true
	original.ChatOps.Platforms.Slack.AllowedTeamIDs = []string{"T123"}
	original.ChatOps.Platforms.Slack.AllowedChannelIDs = []string{"C123"}
	original.ChatOps.Platforms.Discord.Enabled = true
	original.ChatOps.Platforms.Discord.ApplicationID = "app-1"
	original.ChatOps.Platforms.Discord.AllowedGuildIDs = []string{"G123"}
	original.ChatOps.Platforms.Discord.AllowedChannelIDs = []string{"D123"}
	original.ChatOps.Platforms.Telegram.Enabled = true
	original.ChatOps.Platforms.Telegram.AllowedChatIDs = []string{"42"}
	original.ChatOps.Routes = []config.ChatOpsRoute{{
		Platform:    "slack",
		WorkspaceID: "T123",
		ChannelID:   "C123",
		Profile:     "payments-sre",
		Skill:       "incident-context",
		Visibility:  "private",
	}}

	if err := config.Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file permissions.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %o, want 0600", fi.Mode().Perm())
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.DefaultModel != original.DefaultModel {
		t.Errorf("DefaultModel: got %q, want %q", loaded.DefaultModel, original.DefaultModel)
	}
	if loaded.Safety.MaxLogLines != original.Safety.MaxLogLines {
		t.Errorf("MaxLogLines: got %d, want %d", loaded.Safety.MaxLogLines, original.Safety.MaxLogLines)
	}
	if loaded.Safety.AllowSecrets != original.Safety.AllowSecrets {
		t.Errorf("AllowSecrets: got %v, want %v", loaded.Safety.AllowSecrets, original.Safety.AllowSecrets)
	}
	if loaded.Routing.CheapToolStrongSummary != original.Routing.CheapToolStrongSummary {
		t.Errorf("CheapToolStrongSummary: got %v, want %v", loaded.Routing.CheapToolStrongSummary, original.Routing.CheapToolStrongSummary)
	}
	if !loaded.ChatOps.Enabled || loaded.ChatOps.PublicURL != original.ChatOps.PublicURL {
		t.Errorf("ChatOps round trip failed: %#v", loaded.ChatOps)
	}
	if got := loaded.ChatOps.Platforms.Slack.AllowedTeamIDs; len(got) != 1 || got[0] != "T123" {
		t.Errorf("Slack allowed teams = %v, want [T123]", got)
	}
	if got := loaded.ChatOps.Routes; len(got) != 1 || got[0].Skill != "incident-context" {
		t.Errorf("ChatOps routes = %#v", got)
	}
}

func TestChatOpsConfigDoesNotMarshalSecretValues(t *testing.T) {
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Slack.BotTokenEnv = "CLOUDY_SLACK_BOT_TOKEN"
	cfg.ChatOps.Platforms.Telegram.BotTokenEnv = "CLOUDY_TELEGRAM_BOT_TOKEN"

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	for _, token := range []string{"xoxb-", "Bot ", "123456:ABC"} {
		if strings.Contains(text, token) {
			t.Fatalf("config marshaled secret-shaped value %q:\n%s", token, text)
		}
	}
	if !strings.Contains(text, "bot_token_env") {
		t.Fatalf("config should store env var names, got:\n%s", text)
	}
}

func TestTelegramModeMutualExclusion(t *testing.T) {
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Mode = "webhook,polling"

	if err := config.ValidateChatOps(cfg.ChatOps); err == nil {
		t.Fatal("expected combined webhook,polling mode to fail validation")
	}
}

func TestSlackSocketModeRejected(t *testing.T) {
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Slack.Enabled = true
	cfg.ChatOps.Platforms.Slack.Mode = "socket"
	cfg.ChatOps.Platforms.Slack.AllowedTeamIDs = []string{"T1"}
	cfg.ChatOps.Platforms.Slack.AllowedChannelIDs = []string{"C1"}

	if err := config.ValidateChatOps(cfg.ChatOps); err == nil || !strings.Contains(err.Error(), "slack mode must be http") {
		t.Fatalf("ValidateChatOps error = %v, want http-only mode rejection", err)
	}
}

func TestChatOpsEnabledPlatformsRequireExplicitAllowlists(t *testing.T) {
	slack := config.Default().ChatOps
	slack.Enabled = true
	slack.Platforms.Slack.Enabled = true
	if err := config.ValidateChatOps(slack); err == nil || !strings.Contains(err.Error(), "slack requires") {
		t.Fatalf("Slack empty allowlist error = %v", err)
	}
	slack.Platforms.Slack.AllowedTeamIDs = []string{"T1"}
	slack.Platforms.Slack.AllowedChannelIDs = []string{"C1"}
	if err := config.ValidateChatOps(slack); err != nil {
		t.Fatalf("Slack scoped config should validate: %v", err)
	}

	discord := config.Default().ChatOps
	discord.Enabled = true
	discord.Platforms.Discord.Enabled = true
	if err := config.ValidateChatOps(discord); err == nil || !strings.Contains(err.Error(), "discord requires") {
		t.Fatalf("Discord empty allowlist error = %v", err)
	}
	discord.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	discord.Platforms.Discord.AllowedUserIDs = []string{"DU1"}
	if err := config.ValidateChatOps(discord); err != nil {
		t.Fatalf("Discord scoped config should validate: %v", err)
	}

	telegram := config.Default().ChatOps
	telegram.Enabled = true
	telegram.Platforms.Telegram.Enabled = true
	if err := config.ValidateChatOps(telegram); err == nil || !strings.Contains(err.Error(), "telegram requires") {
		t.Fatalf("Telegram empty allowlist error = %v", err)
	}
	telegram.Platforms.Telegram.AllowedUserIDs = []string{"99"}
	if err := config.ValidateChatOps(telegram); err == nil || !strings.Contains(err.Error(), "allowed_chat_ids") {
		t.Fatalf("Telegram user-only allowlist error = %v, want allowed_chat_ids", err)
	}
	telegram.Platforms.Telegram.AllowedChatIDs = []string{"42"}
	if err := config.ValidateChatOps(telegram); err != nil {
		t.Fatalf("Telegram scoped config should validate: %v", err)
	}
}

func TestChatOpsEnvRefsMustBeEnvironmentVariableNames(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*config.ChatOpsConfig)
		label string
	}{
		{
			name: "slack token in bot token env",
			mut: func(c *config.ChatOpsConfig) {
				c.Platforms.Slack.BotTokenEnv = "xoxb-1234567890-secret"
			},
			label: "slack bot_token_env",
		},
		{
			name: "discord public key env starts with digit",
			mut: func(c *config.ChatOpsConfig) {
				c.Platforms.Discord.PublicKeyEnv = "123_PUBLIC_KEY"
			},
			label: "discord public_key_env",
		},
		{
			name: "telegram token in token env",
			mut: func(c *config.ChatOpsConfig) {
				c.Platforms.Telegram.BotTokenEnv = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123"
			},
			label: "telegram bot_token_env",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Default().ChatOps
			c.mut(&cfg)
			err := config.ValidateChatOps(cfg)
			if err == nil || !strings.Contains(err.Error(), c.label) {
				t.Fatalf("ValidateChatOps error = %v, want %s env-name rejection", err, c.label)
			}
		})
	}
}

func TestLoad_MissingFile_ReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load of missing file returned error: %v", err)
	}
	def := config.Default()
	if cfg.DefaultModel != def.DefaultModel {
		t.Errorf("DefaultModel: got %q, want %q", cfg.DefaultModel, def.DefaultModel)
	}
	if cfg.Safety.MaxLogLines != def.Safety.MaxLogLines {
		t.Errorf("MaxLogLines: got %d, want %d", cfg.Safety.MaxLogLines, def.Safety.MaxLogLines)
	}
}

func TestLoad_PartialYAML_OverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.yaml")

	// Only override one field; the rest should stay at defaults.
	partial := "default_model: partial-override-model\n"
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultModel != "partial-override-model" {
		t.Errorf("DefaultModel: got %q, want %q", cfg.DefaultModel, "partial-override-model")
	}
	// Safety defaults must be preserved.
	if cfg.Safety.MaxLogLines != config.Default().Safety.MaxLogLines {
		t.Errorf("MaxLogLines defaulted to %d, want %d", cfg.Safety.MaxLogLines, config.Default().Safety.MaxLogLines)
	}
}

func TestPath_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p := config.Path()
	want := filepath.Join(dir, "cloudy", "config.yaml")
	if p != want {
		t.Errorf("Path with XDG: got %q, want %q", p, want)
	}
}

func TestPath_Default(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	p := config.Path()
	if filepath.Base(p) != "config.yaml" {
		t.Errorf("Path base: got %q, want config.yaml", filepath.Base(p))
	}
}
