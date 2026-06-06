package gateway

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/secrets"
)

func TestStatusToolReportsGatewayState(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Discord.Enabled = true
	cfg.ChatOps.Platforms.Discord.ApplicationID = "123"
	cfg.ChatOps.Platforms.Discord.PublicKeyEnv = "CLOUDY_DISCORD_PUBLIC_KEY"
	cfg.ChatOps.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	cfg.ChatOps.Platforms.Discord.AllowedChannelIDs = []string{"C1"}
	t.Setenv("CLOUDY_DISCORD_PUBLIC_KEY", "abcd")
	if err := config.Save(config.Path(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	obs, err := newStatusTool().Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("gateway.status: %v", err)
	}
	if !strings.Contains(obs.Text, "discord enabled=true ready=true") {
		t.Fatalf("status text missing discord readiness:\n%s", obs.Text)
	}
	if obs.Raw == nil {
		t.Fatal("status raw report was nil")
	}
}

func TestStatusToolReloadsPersistedSecrets(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Mode = "polling"
	cfg.ChatOps.Platforms.Telegram.BotTokenEnv = "CLOUDY_TELEGRAM_BOT_TOKEN"
	cfg.ChatOps.Platforms.Telegram.AllowedChatIDs = []string{"42"}
	if err := config.Save(config.Path(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	if err := secrets.Add("CLOUDY_TELEGRAM_BOT_TOKEN", "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123"); err != nil {
		t.Fatalf("Add secret: %v", err)
	}
	os.Unsetenv("CLOUDY_TELEGRAM_BOT_TOKEN")

	obs, err := newStatusTool().Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("gateway.status: %v", err)
	}
	if !strings.Contains(obs.Text, "telegram enabled=true ready=true") {
		t.Fatalf("status text did not use persisted secret:\n%s", obs.Text)
	}
}
