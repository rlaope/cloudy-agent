package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/chatops"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/agent"
	"github.com/rlaope/cloudy/internal/core/runner"
	"github.com/rlaope/cloudy/internal/permission"
)

func init() { Register(&chatopsCmd{}) }

var telegramWebhookHTTPClient = http.DefaultClient

type chatopsCmd struct{}

func (chatopsCmd) Name() string  { return "chatops" }
func (chatopsCmd) Short() string { return `serve Slack, Discord, and Telegram ChatOps connectors` }

type chatopsOptions struct {
	base baseFlags
	addr string
	url  string
}

func (o *chatopsOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.StringVar(&o.addr, "addr", "", "listen address for chatops serve")
	fs.StringVar(&o.url, "url", "", "public webhook URL for helper commands")
}

func (chatopsCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errf("usage: cloudy chatops <serve|verify-config|telegram-poll|telegram-set-webhook>")
	}
	sub := args[0]
	rest := args[1:]

	var opts chatopsOptions
	parsed, err := parseInto(&opts, "chatops "+sub, rest, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		return errf("config: %w", err)
	}
	if opts.addr != "" {
		cfg.ChatOps.Listen = opts.addr
	}

	switch sub {
	case "verify-config":
		if len(parsed) != 0 {
			return errf("unexpected chatops verify-config argument: %s", parsed[0])
		}
		if err := config.ValidateChatOps(cfg.ChatOps); err != nil {
			return err
		}
		if opts.base.asJSON {
			return json.NewEncoder(stdout).Encode(map[string]any{
				"ok":       true,
				"enabled":  cfg.ChatOps.Enabled,
				"listen":   cfg.ChatOps.Listen,
				"platform": cfg.ChatOps.Platforms,
			})
		}
		fmt.Fprintf(stdout, "chatops config ok; enabled=%v listen=%s\n", cfg.ChatOps.Enabled, cfg.ChatOps.Listen)
		return nil

	case "serve":
		if len(parsed) != 0 {
			return errf("unexpected chatops serve argument: %s", parsed[0])
		}
		if err := requireChatOpsEnabled(cfg); err != nil {
			return err
		}
		service, err := buildChatOpsService(cfg, stderr, opts.base.kubeconfig, opts.base.context)
		if err != nil {
			return err
		}
		mux, err := buildChatOpsMux(cfg, service)
		if err != nil {
			return err
		}
		addr := cfg.ChatOps.Listen
		if addr == "" {
			addr = "127.0.0.1:8787"
		}
		server := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
			_ = service.Shutdown(shutdownCtx)
		}()
		fmt.Fprintf(stderr, "cloudy: chatops listening on %s\n", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil

	case "telegram-poll":
		if len(parsed) != 0 {
			return errf("unexpected chatops telegram-poll argument: %s", parsed[0])
		}
		if err := requireChatOpsEnabled(cfg); err != nil {
			return err
		}
		if cfg.ChatOps.Platforms.Telegram.Mode != "polling" {
			return errf("chatops: telegram-poll requires chatops.platforms.telegram.mode: polling")
		}
		service, err := buildChatOpsService(cfg, stderr, opts.base.kubeconfig, opts.base.context)
		if err != nil {
			return err
		}
		token := os.Getenv(cfg.ChatOps.Platforms.Telegram.BotTokenEnv)
		if token == "" {
			return errf("chatops: %s is not set", cfg.ChatOps.Platforms.Telegram.BotTokenEnv)
		}
		poller := &chatops.TelegramPoller{
			BaseURL:  "https://api.telegram.org",
			BotToken: token,
			Service:  service,
		}
		for {
			if err := poller.PollOnce(ctx); err != nil {
				fmt.Fprintf(stderr, "cloudy: telegram poll: %v\n", err)
			}
			select {
			case <-ctx.Done():
				return service.Shutdown(context.Background())
			case <-time.After(time.Second):
			}
		}

	case "telegram-set-webhook":
		if len(parsed) != 0 {
			return errf("unexpected chatops telegram-set-webhook argument: %s", parsed[0])
		}
		if err := requireChatOpsEnabled(cfg); err != nil {
			return err
		}
		webhookURL := opts.url
		if webhookURL == "" && cfg.ChatOps.PublicURL != "" {
			webhookURL = strings.TrimRight(cfg.ChatOps.PublicURL, "/") + "/chatops/telegram/webhook"
		}
		if webhookURL == "" {
			return errf("usage: cloudy chatops telegram-set-webhook --url https://... or configure chatops.public_url")
		}
		return setTelegramWebhook(ctx, cfg, webhookURL)

	default:
		return errf("unknown chatops subcommand: %s", sub)
	}
}

type cloudyRunner struct {
	stderr         io.Writer
	kubeconfigPath string
	contextName    string
}

func (r cloudyRunner) RunCloudy(ctx context.Context, req chatops.RunRequest) (chatops.RunResult, error) {
	sink := chatops.NewSink()
	res, err := runner.Run(ctx, runner.Request{
		Prompt:         req.Prompt,
		SkillName:      req.SkillName,
		ProfileName:    req.ProfileName,
		ResumeID:       req.ResumeID,
		KubeconfigPath: r.kubeconfigPath,
		ContextName:    r.contextName,
		Sink:           sink,
		Stderr:         r.stderr,
		Approver:       agent.DenyApprover(),
		DefaultMasking: true,
	})
	if err != nil {
		return chatops.RunResult{}, err
	}
	return chatops.RunResult{Text: maskChatText(res.Masker, sink.Text()), Model: res.Model, SessionID: res.SessionID, Masker: res.Masker}, nil
}

func maskChatText(masker chatops.TextMasker, text string) string {
	if masker == nil {
		masker = permission.MaskerOrDefault(nil)
	}
	return masker.MaskString(text)
}

func requireChatOpsEnabled(cfg config.Config) error {
	if !cfg.ChatOps.Enabled {
		return errf("chatops: disabled; set chatops.enabled: true")
	}
	return nil
}

func buildChatOpsService(cfg config.Config, stderr io.Writer, kubeconfigPath, contextName string) (*chatops.Service, error) {
	if err := config.ValidateChatOps(cfg.ChatOps); err != nil {
		return nil, err
	}
	deliveries := chatops.MultiDelivery{}
	if cfg.ChatOps.Platforms.Slack.Enabled {
		tokenEnv := cfg.ChatOps.Platforms.Slack.BotTokenEnv
		if tokenEnv == "" {
			return nil, errf("chatops: slack bot_token_env is required")
		}
		token := os.Getenv(tokenEnv)
		if token == "" {
			return nil, errf("chatops: %s is not set", tokenEnv)
		}
		deliveries[chatops.PlatformSlack] = chatops.SlackDelivery{BotToken: token}
	}
	deliveries[chatops.PlatformDiscord] = chatops.DiscordWebhookDelivery{}
	if tokenEnv := cfg.ChatOps.Platforms.Telegram.BotTokenEnv; tokenEnv != "" {
		token := os.Getenv(tokenEnv)
		if cfg.ChatOps.Platforms.Telegram.Enabled && token == "" {
			return nil, errf("chatops: %s is not set", tokenEnv)
		}
		deliveries[chatops.PlatformTelegram] = chatops.TelegramDelivery{BotToken: token}
	}
	return chatops.NewService(cfg.ChatOps, cloudyRunner{
		stderr:         stderr,
		kubeconfigPath: kubeconfigPath,
		contextName:    contextName,
	}, deliveries, chatops.NewSessionMap(cfg.ChatOps.Session.Path)), nil
}

func buildChatOpsMux(cfg config.Config, service *chatops.Service) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	slack := cfg.ChatOps.Platforms.Slack
	if slack.Enabled {
		secret := os.Getenv(slack.SigningSecretEnv)
		if secret == "" {
			return nil, errf("chatops: %s is not set", slack.SigningSecretEnv)
		}
		adapter := chatops.SlackHTTPAdapter{SigningSecret: secret, Service: service}
		mux.Handle("/chatops/slack/commands", adapter.SlashCommandHandler())
		mux.Handle("/chatops/slack/events", adapter.EventsHandler())
	}
	discord := cfg.ChatOps.Platforms.Discord
	if discord.Enabled {
		key, err := chatops.ParseDiscordPublicKey(os.Getenv(discord.PublicKeyEnv))
		if err != nil {
			return nil, errf("chatops: %s: %w", discord.PublicKeyEnv, err)
		}
		adapter := chatops.DiscordHTTPAdapter{PublicKey: key, ApplicationID: discord.ApplicationID, Service: service}
		mux.Handle("/chatops/discord/interactions", adapter.InteractionsHandler())
	}
	telegram := cfg.ChatOps.Platforms.Telegram
	if telegram.Enabled && telegram.Mode != "polling" {
		if telegram.WebhookSecretEnv == "" {
			return nil, errf("chatops: telegram webhook_secret_env is required in webhook mode")
		}
		secret := os.Getenv(telegram.WebhookSecretEnv)
		if secret == "" {
			return nil, errf("chatops: %s is not set", telegram.WebhookSecretEnv)
		}
		adapter := chatops.TelegramHTTPAdapter{WebhookSecret: secret, Service: service}
		mux.Handle("/chatops/telegram/webhook", adapter.WebhookHandler())
	}
	return mux, nil
}

func setTelegramWebhook(ctx context.Context, cfg config.Config, publicURL string) error {
	telegram := cfg.ChatOps.Platforms.Telegram
	token := os.Getenv(telegram.BotTokenEnv)
	if token == "" {
		return errf("chatops: %s is not set", telegram.BotTokenEnv)
	}
	payload := map[string]any{"url": publicURL}
	if telegram.Mode != "polling" {
		secret := os.Getenv(telegram.WebhookSecretEnv)
		if secret == "" {
			return errf("chatops: %s is not set", telegram.WebhookSecretEnv)
		}
		payload["secret_token"] = secret
	}
	body, _ := json.Marshal(payload)
	url := "https://api.telegram.org/bot" + token + "/setWebhook"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errf("telegram setWebhook: invalid Bot API URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := telegramWebhookHTTPClient.Do(req)
	if err != nil {
		return errf("telegram setWebhook: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram setWebhook: status %d", resp.StatusCode)
	}
	return nil
}
