package gateway

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
)

func TestRunSetupTelegramPollingWritesConfigAndSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	os.Unsetenv("CLOUDY_TELEGRAM_BOT_TOKEN")

	var out bytes.Buffer
	rep, err := RunSetup(t.Context(), SetupOptions{
		Out:        &out,
		ConfigPath: config.Path(),
		AssumeYes:  true,
		Platform:   PlatformTelegram,
		Mode:       "polling",
		BotToken:   "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123",
		ChatIDs:    []string{"42"},
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if !rep.Ready {
		t.Fatalf("report ready = false:\n%s", FormatText(rep))
	}
	if got := os.Getenv("CLOUDY_TELEGRAM_BOT_TOKEN"); got == "" {
		t.Fatal("telegram bot token was not exported")
	}
	cfg, err := config.Load(config.Path())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if !cfg.ChatOps.Enabled || !cfg.ChatOps.Platforms.Telegram.Enabled {
		t.Fatalf("telegram gateway was not enabled: %#v", cfg.ChatOps)
	}
	if cfg.ChatOps.Platforms.Telegram.Mode != "polling" {
		t.Fatalf("telegram mode = %q, want polling", cfg.ChatOps.Platforms.Telegram.Mode)
	}
	if got := cfg.ChatOps.Platforms.Telegram.AllowedChatIDs; len(got) != 1 || got[0] != "42" {
		t.Fatalf("allowed chat IDs = %v, want [42]", got)
	}
	if !strings.Contains(out.String(), "Gateway setup saved.") {
		t.Fatalf("setup output missing success line:\n%s", out.String())
	}
}

func TestRunSetupWithFlagsDoesNotPromptForProvidedValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	os.Unsetenv("CLOUDY_DISCORD_PUBLIC_KEY")

	var out bytes.Buffer
	rep, err := RunSetup(t.Context(), SetupOptions{
		In:                strings.NewReader(""),
		Out:               &out,
		ConfigPath:        config.Path(),
		Platform:          PlatformDiscord,
		Listen:            "127.0.0.1:9999",
		PublicURL:         "https://cloudy.example.test",
		DefaultVisibility: "channel",
		DefaultProfile:    "prod",
		ApplicationID:     "123",
		PublicKey:         validDiscordPublicKey(),
		GuildIDs:          []string{"G1"},
		ChannelIDs:        []string{"C1"},
		UserIDs:           []string{"U1"},
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if !rep.Ready {
		t.Fatalf("report ready = false:\n%s", FormatText(rep))
	}
	text := out.String()
	for _, prompt := range []string{"Listen address", "Discord application ID", "Allowed Discord guild IDs", "Allowed Discord channel IDs"} {
		if strings.Contains(text, prompt) {
			t.Fatalf("setup prompted for provided value %q:\n%s", prompt, text)
		}
	}
}

func TestRunSetupDiscordRejectsInvalidPublicKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	os.Unsetenv("CLOUDY_DISCORD_PUBLIC_KEY")

	var out bytes.Buffer
	_, err := RunSetup(t.Context(), SetupOptions{
		Out:           &out,
		ConfigPath:    config.Path(),
		AssumeYes:     true,
		Platform:      PlatformDiscord,
		ApplicationID: "123",
		PublicKey:     "abcd",
		GuildIDs:      []string{"G1"},
		ChannelIDs:    []string{"C1"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid public key length") {
		t.Fatalf("RunSetup error = %v, want invalid public key length", err)
	}
	if got := os.Getenv("CLOUDY_DISCORD_PUBLIC_KEY"); got != "" {
		t.Fatalf("invalid Discord public key was exported: %q", got)
	}
}

func TestRunSetupRejectsUnknownPlatformWithKnownChoices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)

	_, err := RunSetup(t.Context(), SetupOptions{
		ConfigPath: config.Path(),
		AssumeYes:  true,
		Platform:   "matrix",
	})
	if err == nil {
		t.Fatal("RunSetup error = nil, want unknown platform rejection")
	}
	for _, want := range []string{PlatformSlack, PlatformDiscord, PlatformTelegram} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RunSetup error = %q, want it to list %q", err, want)
		}
	}
}

func TestStatusReportsAllKnownPlatformsInStableOrder(t *testing.T) {
	rep := Status(config.Default())
	got := make([]string, 0, len(rep.Platforms))
	for _, platform := range rep.Platforms {
		got = append(got, platform.Platform)
	}
	want := []string{PlatformSlack, PlatformDiscord, PlatformTelegram}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("platform order = %v, want %v", got, want)
	}
}

func TestStatusDiscordDoesNotRequireBotToken(t *testing.T) {
	t.Setenv("CLOUDY_DISCORD_PUBLIC_KEY", validDiscordPublicKey())
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Discord.Enabled = true
	cfg.ChatOps.Platforms.Discord.ApplicationID = "123"
	cfg.ChatOps.Platforms.Discord.PublicKeyEnv = "CLOUDY_DISCORD_PUBLIC_KEY"
	cfg.ChatOps.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	cfg.ChatOps.Platforms.Discord.AllowedChannelIDs = []string{"C1"}

	rep := Status(cfg)
	var discord PlatformReport
	for _, platform := range rep.Platforms {
		if platform.Platform == PlatformDiscord {
			discord = platform
			break
		}
	}
	if !discord.Ready {
		t.Fatalf("discord ready = false:\n%s", FormatText(rep))
	}
	for _, req := range discord.Requirements {
		if strings.Contains(req.Key, "bot") {
			t.Fatalf("discord should not expose a bot-token requirement: %#v", req)
		}
	}
	if !strings.Contains(strings.Join(discord.Notes, "\n"), "bot tokens are not required") {
		t.Fatalf("discord notes did not explain token requirement: %#v", discord.Notes)
	}
}

func TestStatusDiscordRejectsInvalidPublicKey(t *testing.T) {
	t.Setenv("CLOUDY_DISCORD_PUBLIC_KEY", "abcd")
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Discord.Enabled = true
	cfg.ChatOps.Platforms.Discord.ApplicationID = "123"
	cfg.ChatOps.Platforms.Discord.PublicKeyEnv = "CLOUDY_DISCORD_PUBLIC_KEY"
	cfg.ChatOps.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	cfg.ChatOps.Platforms.Discord.AllowedChannelIDs = []string{"C1"}

	rep := Status(cfg)
	if rep.Ready {
		t.Fatalf("gateway should not be ready with invalid Discord public key:\n%s", FormatText(rep))
	}
	text := FormatText(rep)
	if !strings.Contains(text, "invalid public key length") {
		t.Fatalf("status did not surface invalid Discord public key:\n%s", text)
	}
	if strings.Contains(text, "abcd") {
		t.Fatalf("status leaked Discord public key value:\n%s", text)
	}
}

func TestStatusTelegramWebhookNeedsPublicURL(t *testing.T) {
	t.Setenv("CLOUDY_TELEGRAM_BOT_TOKEN", "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123")
	t.Setenv("CLOUDY_TELEGRAM_WEBHOOK_SECRET", "secret")
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Mode = "webhook"
	cfg.ChatOps.Platforms.Telegram.AllowedChatIDs = []string{"42"}

	rep := Status(cfg)
	if rep.Ready {
		t.Fatalf("gateway should not be ready without public_url:\n%s", FormatText(rep))
	}
	if !strings.Contains(FormatText(rep), "public_url: missing") {
		t.Fatalf("status did not surface missing public_url:\n%s", FormatText(rep))
	}
}

func validDiscordPublicKey() string {
	return strings.Repeat("a", 64)
}
