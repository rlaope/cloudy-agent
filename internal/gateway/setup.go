package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/rlaope/cloudy/internal/chatops"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/secrets"
)

// SetupOptions controls the interactive gateway setup workflow.
type SetupOptions struct {
	In  io.Reader
	Out io.Writer

	ConfigPath string
	AssumeYes  bool

	Platform string
	Mode     string

	Listen            string
	PublicURL         string
	DefaultVisibility string
	DefaultProfile    string
	Skill             string

	SigningSecret string
	BotToken      string
	PublicKey     string
	WebhookSecret string
	ApplicationID string

	TeamIDs    []string
	GuildIDs   []string
	ChatIDs    []string
	ChannelIDs []string
	UserIDs    []string
}

// RunSetup prompts for the minimum platform-specific inputs, writes config and
// secrets, and returns the resulting status report.
func RunSetup(_ context.Context, opts SetupOptions) (Report, error) {
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	path := opts.ConfigPath
	if path == "" {
		path = config.Path()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return Report{}, fmt.Errorf("gateway setup: config: %w", err)
	}
	reader := bufio.NewReader(in)
	w := setupWizard{in: in, reader: reader, out: out, opts: opts}

	platform := strings.ToLower(strings.TrimSpace(opts.Platform))
	if platform == "" {
		platform = strings.ToLower(w.prompt("Platform [slack/discord/telegram]", "telegram"))
	}
	switch platform {
	case PlatformSlack, PlatformDiscord, PlatformTelegram:
	default:
		return Report{}, fmt.Errorf("gateway setup: platform must be slack, discord, or telegram")
	}

	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Listen = w.choosePrompt(opts.Listen, "Listen address", firstNonEmpty(cfg.ChatOps.Listen, "127.0.0.1:8787"))
	cfg.ChatOps.DefaultVisibility = w.choosePrompt(opts.DefaultVisibility, "Default visibility [private/channel]", firstNonEmpty(cfg.ChatOps.DefaultVisibility, "private"))
	cfg.ChatOps.DefaultProfile = w.choosePrompt(opts.DefaultProfile, "Default Cloudy profile (optional)", cfg.ChatOps.DefaultProfile)
	if opts.PublicURL != "" {
		cfg.ChatOps.PublicURL = opts.PublicURL
	}

	switch platform {
	case PlatformSlack:
		if err := w.setupSlack(&cfg); err != nil {
			return Report{}, err
		}
	case PlatformDiscord:
		if err := w.setupDiscord(&cfg); err != nil {
			return Report{}, err
		}
	case PlatformTelegram:
		if err := w.setupTelegram(&cfg); err != nil {
			return Report{}, err
		}
	}
	if err := config.ValidateChatOps(cfg.ChatOps); err != nil {
		return Report{}, err
	}
	if err := config.Save(path, cfg); err != nil {
		return Report{}, fmt.Errorf("gateway setup: save config: %w", err)
	}
	rep := Status(cfg)
	fmt.Fprintln(out, "\nGateway setup saved.")
	fmt.Fprint(out, FormatText(rep))
	return rep, nil
}

type setupWizard struct {
	in     io.Reader
	reader *bufio.Reader
	out    io.Writer
	opts   SetupOptions
}

func (w setupWizard) setupSlack(cfg *config.Config) error {
	slack := cfg.ChatOps.Platforms.Slack
	slack.Enabled = true
	slack.Mode = "http"
	slack.SigningSecretEnv = firstNonEmpty(slack.SigningSecretEnv, "CLOUDY_SLACK_SIGNING_SECRET")
	slack.BotTokenEnv = firstNonEmpty(slack.BotTokenEnv, "CLOUDY_SLACK_BOT_TOKEN")
	fmt.Fprintln(w.out, "Slack HTTP mode requires a signing secret, bot token, team ID, and channel/user allowlist.")
	cfg.ChatOps.PublicURL = w.choosePrompt(w.opts.PublicURL, "Public base URL (optional)", cfg.ChatOps.PublicURL)
	if err := w.ensureSecret(slack.SigningSecretEnv, "Slack signing secret", w.opts.SigningSecret, false); err != nil {
		return err
	}
	if err := w.ensureSecret(slack.BotTokenEnv, "Slack bot token", w.opts.BotToken, false); err != nil {
		return err
	}
	slack.AllowedTeamIDs = w.chooseListPrompt(w.opts.TeamIDs, "Allowed Slack team IDs", slack.AllowedTeamIDs)
	slack.AllowedChannelIDs = w.chooseListPrompt(w.opts.ChannelIDs, "Allowed Slack channel IDs", slack.AllowedChannelIDs)
	slack.AllowedUserIDs = w.chooseListPrompt(w.opts.UserIDs, "Allowed Slack user IDs (optional)", slack.AllowedUserIDs)
	cfg.ChatOps.Platforms.Slack = slack
	cfg.ChatOps.Routes = upsertRoute(cfg.ChatOps.Routes, config.ChatOpsRoute{
		Platform:    PlatformSlack,
		WorkspaceID: first(slack.AllowedTeamIDs),
		ChannelID:   first(slack.AllowedChannelIDs),
		UserID:      first(slack.AllowedUserIDs),
		Profile:     cfg.ChatOps.DefaultProfile,
		Skill:       w.opts.Skill,
		Visibility:  cfg.ChatOps.DefaultVisibility,
	})
	return nil
}

func (w setupWizard) setupDiscord(cfg *config.Config) error {
	discord := cfg.ChatOps.Platforms.Discord
	discord.Enabled = true
	discord.PublicKeyEnv = firstNonEmpty(discord.PublicKeyEnv, "CLOUDY_DISCORD_PUBLIC_KEY")
	fmt.Fprintln(w.out, "Discord interactions mode does not require a bot token; it requires application ID, public key, guild ID, and channel/user allowlist.")
	cfg.ChatOps.PublicURL = w.choosePrompt(w.opts.PublicURL, "Public base URL (optional)", cfg.ChatOps.PublicURL)
	discord.ApplicationID = w.choosePrompt(w.opts.ApplicationID, "Discord application ID", discord.ApplicationID)
	if discord.ApplicationID == "" {
		return fmt.Errorf("gateway setup: Discord application ID is required")
	}
	if err := w.ensureDiscordPublicKey(discord.PublicKeyEnv, w.opts.PublicKey); err != nil {
		return err
	}
	discord.AllowedGuildIDs = w.chooseListPrompt(w.opts.GuildIDs, "Allowed Discord guild IDs", discord.AllowedGuildIDs)
	discord.AllowedChannelIDs = w.chooseListPrompt(w.opts.ChannelIDs, "Allowed Discord channel IDs", discord.AllowedChannelIDs)
	discord.AllowedUserIDs = w.chooseListPrompt(w.opts.UserIDs, "Allowed Discord user IDs (optional)", discord.AllowedUserIDs)
	cfg.ChatOps.Platforms.Discord = discord
	cfg.ChatOps.Routes = upsertRoute(cfg.ChatOps.Routes, config.ChatOpsRoute{
		Platform:   PlatformDiscord,
		GuildID:    first(discord.AllowedGuildIDs),
		ChannelID:  first(discord.AllowedChannelIDs),
		UserID:     first(discord.AllowedUserIDs),
		Profile:    cfg.ChatOps.DefaultProfile,
		Skill:      w.opts.Skill,
		Visibility: cfg.ChatOps.DefaultVisibility,
	})
	return nil
}

func (w setupWizard) setupTelegram(cfg *config.Config) error {
	telegram := cfg.ChatOps.Platforms.Telegram
	telegram.Enabled = true
	telegram.Mode = strings.ToLower(w.choosePrompt(w.opts.Mode, "Telegram mode [webhook/polling]", firstNonEmpty(telegram.Mode, "webhook")))
	if telegram.Mode == "" {
		telegram.Mode = "webhook"
	}
	telegram.BotTokenEnv = firstNonEmpty(telegram.BotTokenEnv, "CLOUDY_TELEGRAM_BOT_TOKEN")
	telegram.WebhookSecretEnv = firstNonEmpty(telegram.WebhookSecretEnv, "CLOUDY_TELEGRAM_WEBHOOK_SECRET")
	if err := w.ensureSecret(telegram.BotTokenEnv, "Telegram bot token", w.opts.BotToken, false); err != nil {
		return err
	}
	if telegram.Mode != "polling" {
		cfg.ChatOps.PublicURL = w.choosePrompt(w.opts.PublicURL, "Public base URL", cfg.ChatOps.PublicURL)
		if cfg.ChatOps.PublicURL == "" {
			return fmt.Errorf("gateway setup: public URL is required for Telegram webhook mode")
		}
		secret := w.opts.WebhookSecret
		if secret == "" && os.Getenv(telegram.WebhookSecretEnv) == "" {
			secret = w.promptSecret("Telegram webhook secret (blank to generate)")
			if secret == "" {
				generated, err := randomSecret()
				if err != nil {
					return err
				}
				secret = generated
				fmt.Fprintln(w.out, "Generated Telegram webhook secret.")
			}
		}
		if err := w.ensureSecret(telegram.WebhookSecretEnv, "Telegram webhook secret", secret, true); err != nil {
			return err
		}
	}
	telegram.AllowedChatIDs = w.chooseListPrompt(w.opts.ChatIDs, "Allowed Telegram chat IDs", telegram.AllowedChatIDs)
	telegram.AllowedUserIDs = w.chooseListPrompt(w.opts.UserIDs, "Allowed Telegram user IDs (optional)", telegram.AllowedUserIDs)
	cfg.ChatOps.Platforms.Telegram = telegram
	cfg.ChatOps.Routes = upsertRoute(cfg.ChatOps.Routes, config.ChatOpsRoute{
		Platform:   PlatformTelegram,
		ChatID:     first(telegram.AllowedChatIDs),
		UserID:     first(telegram.AllowedUserIDs),
		Profile:    cfg.ChatOps.DefaultProfile,
		Skill:      w.opts.Skill,
		Visibility: cfg.ChatOps.DefaultVisibility,
	})
	return nil
}

func (w setupWizard) ensureSecret(envName, label, value string, allowGenerated bool) error {
	if envName == "" {
		return fmt.Errorf("gateway setup: env var name for %s is empty", label)
	}
	if value == "" && os.Getenv(envName) == "" {
		value = w.promptSecret(label)
	}
	if value == "" && os.Getenv(envName) != "" {
		fmt.Fprintf(w.out, "Using existing %s from %s.\n", label, envName)
		return nil
	}
	if value == "" {
		if allowGenerated {
			return nil
		}
		return fmt.Errorf("gateway setup: %s is required", label)
	}
	if err := secrets.Add(envName, value); err != nil {
		return fmt.Errorf("gateway setup: save %s: %w", envName, err)
	}
	return nil
}

func (w setupWizard) ensureDiscordPublicKey(envName, value string) error {
	const label = "Discord public key"
	if envName == "" {
		return fmt.Errorf("gateway setup: env var name for %s is empty", label)
	}
	if value == "" && os.Getenv(envName) == "" {
		value = w.promptSecret(label)
	}
	if value == "" && os.Getenv(envName) != "" {
		if _, err := chatops.ParseDiscordPublicKey(os.Getenv(envName)); err != nil {
			return fmt.Errorf("gateway setup: %s: %w", envName, err)
		}
		fmt.Fprintf(w.out, "Using existing %s from %s.\n", label, envName)
		return nil
	}
	if value == "" {
		return fmt.Errorf("gateway setup: %s is required", label)
	}
	if _, err := chatops.ParseDiscordPublicKey(value); err != nil {
		return fmt.Errorf("gateway setup: %s: %w", envName, err)
	}
	if err := secrets.Add(envName, value); err != nil {
		return fmt.Errorf("gateway setup: save %s: %w", envName, err)
	}
	return nil
}

func (w setupWizard) prompt(label, def string) string {
	if w.opts.AssumeYes {
		return def
	}
	if def != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w.out, "%s: ", label)
	}
	line, _ := w.reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func (w setupWizard) promptList(label string, def []string) []string {
	return splitList(w.prompt(label, strings.Join(def, ",")))
}

func (w setupWizard) promptSecret(label string) string {
	if w.opts.AssumeYes {
		return ""
	}
	if file, ok := w.in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintf(w.out, "%s: ", label)
		b, _ := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(w.out)
		return strings.TrimSpace(string(b))
	}
	return w.prompt(label, "")
}

func (w setupWizard) choosePrompt(value, label, def string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(w.prompt(label, def))
}

func (w setupWizard) chooseListPrompt(values []string, label string, def []string) []string {
	if len(values) > 0 {
		return cleanList(values)
	}
	return w.promptList(label, def)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return cleanList(strings.Split(s, ","))
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func upsertRoute(routes []config.ChatOpsRoute, next config.ChatOpsRoute) []config.ChatOpsRoute {
	if next.Platform == "" {
		return routes
	}
	for i, route := range routes {
		if route.Platform == next.Platform {
			routes[i] = next
			return routes
		}
	}
	return append(routes, next)
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
