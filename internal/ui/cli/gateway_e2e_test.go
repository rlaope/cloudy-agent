package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

func TestGatewaySetupStatusAndVerifyConfigAcrossMessengers(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	setupRuns := [][]string{
		{
			"setup",
			"--yes",
			"--platform", "slack",
			"--signing-secret", "slack-signing-secret",
			"--bot-token", "xoxb-ultraqa-test-token",
			"--team-id", "T1",
			"--channel-id", "C1",
		},
		{
			"setup",
			"--yes",
			"--platform", "discord",
			"--application-id", "app1",
			"--public-key", hex.EncodeToString(pub),
			"--guild-id", "G1",
			"--channel-id", "D1",
		},
		{
			"setup",
			"--yes",
			"--platform", "telegram",
			"--mode", "webhook",
			"--public-url", "https://cloudy.example",
			"--bot-token", "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123",
			"--webhook-secret", "telegram-webhook-secret",
			"--chat-id", "42",
		},
	}
	for _, args := range setupRuns {
		var out bytes.Buffer
		if err := (gatewayCmd{}).Run(context.Background(), args, &out, io.Discard); err != nil {
			t.Fatalf("gateway %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		if !strings.Contains(out.String(), "Gateway setup saved.") {
			t.Fatalf("gateway setup output missing saved marker:\n%s", out.String())
		}
	}

	var verifyOut bytes.Buffer
	if err := (chatopsCmd{}).Run(context.Background(), []string{"verify-config", "--json"}, &verifyOut, io.Discard); err != nil {
		t.Fatalf("chatops verify-config: %v\n%s", err, verifyOut.String())
	}
	var verify map[string]any
	if err := json.Unmarshal(verifyOut.Bytes(), &verify); err != nil {
		t.Fatalf("verify-config output is not JSON: %v\n%s", err, verifyOut.String())
	}
	if verify["ok"] != true || verify["enabled"] != true {
		t.Fatalf("verify-config = %#v, want ok/enabled true", verify)
	}

	var statusOut bytes.Buffer
	if err := (gatewayCmd{}).Run(context.Background(), []string{"status", "--json"}, &statusOut, io.Discard); err != nil {
		t.Fatalf("gateway status: %v\n%s", err, statusOut.String())
	}
	statusText := statusOut.String()
	for _, secret := range []string{"slack-signing-secret", "xoxb-ultraqa-test-token", "telegram-webhook-secret", "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123"} {
		if strings.Contains(statusText, secret) {
			t.Fatalf("gateway status leaked secret %q:\n%s", secret, statusText)
		}
	}
	var status struct {
		Ready     bool `json:"ready"`
		Platforms []struct {
			Platform string `json:"platform"`
			Ready    bool   `json:"ready"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(statusOut.Bytes(), &status); err != nil {
		t.Fatalf("gateway status output is not JSON: %v\n%s", err, statusOut.String())
	}
	if !status.Ready {
		t.Fatalf("gateway status ready=false:\n%s", statusOut.String())
	}
	ready := map[string]bool{}
	for _, platform := range status.Platforms {
		ready[platform.Platform] = platform.Ready
	}
	for _, platform := range []string{"slack", "discord", "telegram"} {
		if !ready[platform] {
			t.Fatalf("platform %s ready=false in status:\n%s", platform, statusOut.String())
		}
	}

	data, err := os.ReadFile(filepath.Join(os.Getenv("CLOUDY_HOME"), "secrets"))
	if err != nil {
		t.Fatalf("Read secrets: %v", err)
	}
	if info, err := os.Stat(filepath.Join(os.Getenv("CLOUDY_HOME"), "secrets")); err != nil {
		t.Fatalf("Stat secrets: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets mode = %v, want 0600", info.Mode().Perm())
	}
	for _, key := range []string{"CLOUDY_SLACK_BOT_TOKEN=", "CLOUDY_DISCORD_PUBLIC_KEY=", "CLOUDY_TELEGRAM_BOT_TOKEN="} {
		if !bytes.Contains(data, []byte(key)) {
			t.Fatalf("secrets file missing %s", key)
		}
	}
}

func TestChatOpsServeRejectsUnauthenticatedLocalMessengerRequests(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	t.Setenv("CLOUDY_SLACK_SIGNING_SECRET", "slack-signing-secret")
	t.Setenv("CLOUDY_SLACK_BOT_TOKEN", "xoxb-ultraqa-test-token")
	t.Setenv("CLOUDY_DISCORD_PUBLIC_KEY", hex.EncodeToString(pub))
	t.Setenv("CLOUDY_TELEGRAM_BOT_TOKEN", "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123")
	t.Setenv("CLOUDY_TELEGRAM_WEBHOOK_SECRET", "telegram-webhook-secret")

	addr := freeLocalAddr(t)
	cfg := config.Default()
	cfg.ChatOps.Enabled = true
	cfg.ChatOps.Listen = addr
	cfg.ChatOps.Platforms.Slack.Enabled = true
	cfg.ChatOps.Platforms.Slack.AllowedTeamIDs = []string{"T1"}
	cfg.ChatOps.Platforms.Slack.AllowedChannelIDs = []string{"C1"}
	cfg.ChatOps.Platforms.Discord.Enabled = true
	cfg.ChatOps.Platforms.Discord.ApplicationID = "app1"
	cfg.ChatOps.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	cfg.ChatOps.Platforms.Discord.AllowedChannelIDs = []string{"D1"}
	cfg.ChatOps.Platforms.Telegram.Enabled = true
	cfg.ChatOps.Platforms.Telegram.Mode = "webhook"
	cfg.ChatOps.Platforms.Telegram.AllowedChatIDs = []string{"42"}
	if err := config.Save(config.Path(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	var stderr bytes.Buffer
	go func() {
		errCh <- (chatopsCmd{}).Run(ctx, []string{"serve"}, io.Discard, &stderr)
	}()
	baseURL := "http://" + addr
	waitForServe(t, baseURL, errCh)

	client := &http.Client{Timeout: time.Second}
	checkPostStatus(t, client, baseURL+"/chatops/slack/commands", "team_id=T1&channel_id=C1&user_id=U1&text=ask+hi", nil, http.StatusUnauthorized)
	checkPostStatus(t, client, baseURL+"/chatops/discord/interactions", `{"type":1}`, nil, http.StatusUnauthorized)
	checkPostStatus(t, client, baseURL+"/chatops/telegram/webhook", `{"update_id":1,`, map[string]string{
		"X-Telegram-Bot-Api-Secret-Token": "telegram-webhook-secret",
	}, http.StatusBadRequest)
	checkPostStatus(t, client, baseURL+"/chatops/telegram/webhook", `{"update_id":1}`, map[string]string{
		"X-Telegram-Bot-Api-Secret-Token": "wrong",
	}, http.StatusUnauthorized)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("chatops serve returned error: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("chatops serve did not stop after cancellation; stderr:\n%s", stderr.String())
	}
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}
	return addr
}

func waitForServe(t *testing.T, baseURL string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("chatops serve exited before readiness: %v", err)
		default:
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+"/chatops/telegram/webhook", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for chatops serve at %s", baseURL)
}

func checkPostStatus(t *testing.T, client *http.Client, url string, body string, headers map[string]string, want int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("POST %s status = %d, want %d", url, resp.StatusCode, want)
	}
}
