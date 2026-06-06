package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/gateway"
	"github.com/rlaope/cloudy/internal/secrets"
)

func init() { Register(&gatewayCmd{}) }

type gatewayCmd struct{}

func (gatewayCmd) Name() string  { return "gateway" }
func (gatewayCmd) Short() string { return `configure and inspect ChatOps gateway setup` }

type gatewayOptions struct {
	base baseFlags

	yes               bool
	platform          string
	mode              string
	listen            string
	publicURL         string
	defaultVisibility string
	defaultProfile    string
	skill             string
	signingSecret     string
	botToken          string
	publicKey         string
	webhookSecret     string
	applicationID     string
	teamIDs           csvFlag
	guildIDs          csvFlag
	chatIDs           csvFlag
	channelIDs        csvFlag
	userIDs           csvFlag
}

func (o *gatewayOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.BoolVar(&o.yes, "yes", false, "accept defaults and fail instead of prompting for missing required values")
	fs.StringVar(&o.platform, "platform", "", "platform to configure: slack, discord, or telegram")
	fs.StringVar(&o.mode, "mode", "", "platform mode where applicable: webhook or polling")
	fs.StringVar(&o.listen, "listen", "", "gateway listen address")
	fs.StringVar(&o.publicURL, "public-url", "", "public base URL for webhook platforms")
	fs.StringVar(&o.defaultVisibility, "visibility", "", "default response visibility: private or channel")
	fs.StringVar(&o.defaultProfile, "profile", "", "default Cloudy profile for gateway runs")
	fs.StringVar(&o.skill, "skill", "", "default Cloudy skill for this platform route")
	fs.StringVar(&o.signingSecret, "signing-secret", "", "Slack signing secret value to save in ~/.cloudy/secrets")
	fs.StringVar(&o.botToken, "bot-token", "", "Slack or Telegram bot token value to save in ~/.cloudy/secrets")
	fs.StringVar(&o.publicKey, "public-key", "", "Discord public key value to save in ~/.cloudy/secrets")
	fs.StringVar(&o.webhookSecret, "webhook-secret", "", "Telegram webhook secret value to save in ~/.cloudy/secrets")
	fs.StringVar(&o.applicationID, "application-id", "", "Discord application ID")
	fs.Var(&o.teamIDs, "team-id", "allowed Slack team/workspace ID; repeat or comma-separate")
	fs.Var(&o.guildIDs, "guild-id", "allowed Discord guild/server ID; repeat or comma-separate")
	fs.Var(&o.chatIDs, "chat-id", "allowed Telegram chat ID; repeat or comma-separate")
	fs.Var(&o.channelIDs, "channel-id", "allowed Slack/Discord channel ID; repeat or comma-separate")
	fs.Var(&o.userIDs, "user-id", "allowed user ID; repeat or comma-separate")
}

func (gatewayCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy gateway <setup|status>")
	}
	sub := args[0]
	rest := args[1:]

	var opts gatewayOptions
	parsed, err := parseInto(&opts, "gateway "+sub, rest, stderr)
	if err != nil {
		return err
	}
	if len(parsed) != 0 {
		return errf("unexpected gateway %s argument: %s", sub, parsed[0])
	}

	switch sub {
	case "setup":
		_, err := gateway.RunSetup(ctx, gateway.SetupOptions{
			In:                os.Stdin,
			Out:               stdout,
			ConfigPath:        config.Path(),
			AssumeYes:         opts.yes,
			Platform:          opts.platform,
			Mode:              opts.mode,
			Listen:            opts.listen,
			PublicURL:         opts.publicURL,
			DefaultVisibility: opts.defaultVisibility,
			DefaultProfile:    opts.defaultProfile,
			Skill:             opts.skill,
			SigningSecret:     opts.signingSecret,
			BotToken:          opts.botToken,
			PublicKey:         opts.publicKey,
			WebhookSecret:     opts.webhookSecret,
			ApplicationID:     opts.applicationID,
			TeamIDs:           opts.teamIDs.Values(),
			GuildIDs:          opts.guildIDs.Values(),
			ChatIDs:           opts.chatIDs.Values(),
			ChannelIDs:        opts.channelIDs.Values(),
			UserIDs:           opts.userIDs.Values(),
		})
		return err

	case "status":
		if err := secrets.Load(); err != nil {
			return fmt.Errorf("gateway status: %w", err)
		}
		cfg, err := config.Load(config.Path())
		if err != nil {
			return err
		}
		rep := gateway.Status(cfg)
		if opts.base.asJSON {
			return json.NewEncoder(stdout).Encode(rep)
		}
		fmt.Fprint(stdout, gateway.FormatText(rep))
		return nil

	default:
		return errf("unknown gateway subcommand: %s", sub)
	}
}

type csvFlag []string

func (f *csvFlag) String() string {
	if f == nil {
		return ""
	}
	return fmt.Sprint([]string(*f))
}

func (f *csvFlag) Set(value string) error {
	for _, part := range gatewaySplit(value) {
		*f = append(*f, part)
	}
	return nil
}

func (f *csvFlag) Values() []string {
	out := make([]string, 0, len(*f))
	seen := map[string]bool{}
	for _, value := range *f {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func gatewaySplit(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
