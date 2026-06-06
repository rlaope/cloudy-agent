package chatops

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlackVerifySignatureValidAndRejectsInvalid(t *testing.T) {
	secret := "signing-secret"
	body := []byte("team_id=T1&channel_id=C1&user_id=U1&text=ask+pods")
	now := time.Unix(1700000000, 0)
	ts := "1700000000"
	sig := SignSlackTestBody(secret, ts, body)
	if !VerifySlackSignature(secret, ts, body, sig, now) {
		t.Fatal("valid Slack signature rejected")
	}
	if VerifySlackSignature(secret, ts, []byte("tampered"), sig, now) {
		t.Fatal("tampered Slack body accepted")
	}
	if VerifySlackSignature(secret, "1699990000", body, sig, now) {
		t.Fatal("stale Slack timestamp accepted")
	}
}

func TestSlackSlashHandlerEmitsNormalizedEventOnlyAfterVerification(t *testing.T) {
	gotReq := make(chan RunRequest, 1)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(_ context.Context, req RunRequest) (RunResult, error) {
		gotReq <- req
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	now := time.Unix(1700000000, 0)
	adapter := SlackHTTPAdapter{SigningSecret: "secret", Service: service, Now: func() time.Time { return now }}
	form := url.Values{
		"team_id":      {"T1"},
		"channel_id":   {"C1"},
		"user_id":      {"U1"},
		"command":      {"/cloudy"},
		"text":         {"ask why are pods pending?"},
		"trigger_id":   {"trig-1"},
		"response_url": {"https://slack.example/response"},
	}
	body := []byte(form.Encode())
	req := httptest.NewRequest(http.MethodPost, "/chatops/slack/commands", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	req.Header.Set("X-Slack-Signature", SignSlackTestBody("secret", "1700000000", body))
	rr := httptest.NewRecorder()

	adapter.SlashCommandHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	got := waitReq(t, gotReq)
	if got.Prompt != "why are pods pending?" {
		t.Fatalf("prompt = %q", got.Prompt)
	}

	bad := httptest.NewRequest(http.MethodPost, "/chatops/slack/commands", bytes.NewReader(body))
	bad.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	bad.Header.Set("X-Slack-Signature", "v0=bad")
	rr = httptest.NewRecorder()
	adapter.SlashCommandHandler().ServeHTTP(rr, bad)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d, want 401", rr.Code)
	}

	form.Set("channel_id", "C-denied")
	deniedBody := []byte(form.Encode())
	denied := httptest.NewRequest(http.MethodPost, "/chatops/slack/commands", bytes.NewReader(deniedBody))
	denied.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	denied.Header.Set("X-Slack-Signature", SignSlackTestBody("secret", "1700000000", deniedBody))
	rr = httptest.NewRecorder()
	adapter.SlashCommandHandler().ServeHTTP(rr, denied)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "not allowed") {
		t.Fatalf("denied response = %d %q", rr.Code, rr.Body.String())
	}
}

func TestSlackEventsIgnoresSignedNonMention(t *testing.T) {
	called := make(chan struct{}, 1)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		called <- struct{}{}
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	now := time.Unix(1700000000, 0)
	adapter := SlackHTTPAdapter{SigningSecret: "secret", Service: service, Now: func() time.Time { return now }}
	body := []byte(`{"type":"event_callback","team_id":"T1","event_id":"E1","event":{"type":"message","text":"ordinary channel text","user":"U1","channel":"C1","ts":"123.45"}}`)
	req := httptest.NewRequest(http.MethodPost, "/chatops/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	req.Header.Set("X-Slack-Signature", SignSlackTestBody("secret", "1700000000", body))
	rr := httptest.NewRecorder()
	adapter.EventsHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-called:
		t.Fatal("runner was called for signed non-app_mention event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlackEventsAppMentionDeliversWithBotToken(t *testing.T) {
	delivered := make(chan map[string]any, 1)
	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postEphemeral" {
			t.Fatalf("path = %s, want /chat.postEphemeral", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer xoxb-test-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		delivered <- payload
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer slackAPI.Close()

	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), SlackDelivery{BotToken: "xoxb-test-token", BaseURL: slackAPI.URL}, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	now := time.Unix(1700000000, 0)
	adapter := SlackHTTPAdapter{SigningSecret: "secret", Service: service, Now: func() time.Time { return now }}
	body := []byte(`{"type":"event_callback","team_id":"T1","event_id":"E2","event":{"type":"app_mention","text":"<@Ubot> why?","user":"U1","channel":"C1","thread_ts":"123.45","ts":"123.45"}}`)
	req := httptest.NewRequest(http.MethodPost, "/chatops/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	req.Header.Set("X-Slack-Signature", SignSlackTestBody("secret", "1700000000", body))
	rr := httptest.NewRecorder()
	adapter.EventsHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case payload := <-delivered:
		if payload["channel"] != "C1" || payload["thread_ts"] != "123.45" {
			t.Fatalf("payload = %#v", payload)
		}
		if payload["user"] != "U1" {
			t.Fatalf("payload user = %#v, want U1", payload["user"])
		}
		if !strings.Contains(payload["text"].(string), "answer") {
			t.Fatalf("payload text = %#v", payload["text"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Slack bot delivery")
	}
}

func TestSlackBotDeliveryHonorsChannelVisibility(t *testing.T) {
	delivered := make(chan string, 1)
	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer slackAPI.Close()

	err := (SlackDelivery{BotToken: "xoxb-test-token", BaseURL: slackAPI.URL}).Deliver(context.Background(), ReplyTarget{
		Platform:  PlatformSlack,
		ChannelID: "C1",
		UserID:    "U1",
	}, Message{Text: "channel", Visibility: VisibilityChannel})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := <-delivered; got != "/chat.postMessage" {
		t.Fatalf("Slack path = %s, want /chat.postMessage", got)
	}
}

func TestDeliveryErrorsDoNotLeakSecretURLs(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial failed for %s", req.URL.String())
	})}
	msg := Message{Text: "answer"}

	slackURL := "https://hooks.slack.example/response/secret-token"
	if err := (SlackDelivery{Client: client}).Deliver(context.Background(), ReplyTarget{Platform: PlatformSlack, ResponseURL: slackURL}, msg); err == nil {
		t.Fatal("Slack delivery returned nil error")
	} else if strings.Contains(err.Error(), slackURL) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("Slack delivery error leaked response URL: %v", err)
	}

	discordToken := "discord-secret-interaction-token"
	if err := (DiscordWebhookDelivery{Client: client, Base: "https://discord.example/api"}).Deliver(context.Background(), ReplyTarget{Platform: PlatformDiscord, ApplicationID: "app1", InteractionToken: discordToken}, msg); err == nil {
		t.Fatal("Discord delivery returned nil error")
	} else if strings.Contains(err.Error(), discordToken) {
		t.Fatalf("Discord delivery error leaked interaction token: %v", err)
	}

	telegramToken := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_123"
	if err := (TelegramDelivery{Client: client, BaseURL: "https://telegram.example", BotToken: telegramToken}).Deliver(context.Background(), ReplyTarget{Platform: PlatformTelegram, ChatID: "42"}, msg); err == nil {
		t.Fatal("Telegram delivery returned nil error")
	} else if strings.Contains(err.Error(), telegramToken) {
		t.Fatalf("Telegram delivery error leaked bot token: %v", err)
	}

	poller := &TelegramPoller{Client: client, BaseURL: "https://telegram.example", BotToken: telegramToken}
	if err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("Telegram poller returned nil error")
	} else if strings.Contains(err.Error(), telegramToken) {
		t.Fatalf("Telegram poller error leaked bot token: %v", err)
	}
}

func TestDiscordVerifySignatureAndPing(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	body := []byte(`{"type":1}`)
	ts := "1700000000"
	sig := ed25519.Sign(priv, append([]byte(ts), body...))
	if !VerifyDiscordSignature(pub, ts, body, hex.EncodeToString(sig)) {
		t.Fatal("valid Discord signature rejected")
	}
	if VerifyDiscordSignature(pub, ts, []byte(`{"type":2}`), hex.EncodeToString(sig)) {
		t.Fatal("tampered Discord body accepted")
	}

	service := NewService(testChatOpsConfig(), nil, nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)
	now := time.Unix(1700000000, 0)
	adapter := DiscordHTTPAdapter{PublicKey: pub, ApplicationID: "app1", Service: service, Now: func() time.Time { return now }}
	req := httptest.NewRequest(http.MethodPost, "/chatops/discord/interactions", bytes.NewReader(body))
	req.Header.Set("X-Signature-Timestamp", ts)
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	rr := httptest.NewRecorder()
	adapter.InteractionsHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":1`) {
		t.Fatalf("PING response = %s", rr.Body.String())
	}

	staleTS := "1699990000"
	staleSig := ed25519.Sign(priv, append([]byte(staleTS), body...))
	stale := httptest.NewRequest(http.MethodPost, "/chatops/discord/interactions", bytes.NewReader(body))
	stale.Header.Set("X-Signature-Timestamp", staleTS)
	stale.Header.Set("X-Signature-Ed25519", hex.EncodeToString(staleSig))
	rr = httptest.NewRecorder()
	adapter.InteractionsHandler().ServeHTTP(rr, stale)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("stale Discord signature status = %d, want 401", rr.Code)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDiscordSlashHandlerDefersAndEnqueues(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	gotReq := make(chan RunRequest, 1)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(_ context.Context, req RunRequest) (RunResult, error) {
		gotReq <- req
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)
	payload := map[string]any{
		"id":             "i1",
		"application_id": "app1",
		"type":           2,
		"token":          "tok",
		"guild_id":       "G1",
		"channel_id":     "D1",
		"member":         map[string]any{"user": map[string]any{"id": "DU1"}},
		"data": map[string]any{
			"name":    "cloudy",
			"options": []map[string]any{{"name": "question", "value": "why is checkout slow?"}},
		},
	}
	body, _ := json.Marshal(payload)
	ts := "1700000000"
	sig := ed25519.Sign(priv, append([]byte(ts), body...))
	req := httptest.NewRequest(http.MethodPost, "/chatops/discord/interactions", bytes.NewReader(body))
	req.Header.Set("X-Signature-Timestamp", ts)
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	rr := httptest.NewRecorder()

	DiscordHTTPAdapter{
		PublicKey:     pub,
		ApplicationID: "app1",
		Service:       service,
		Now:           func() time.Time { return time.Unix(1700000000, 0) },
	}.InteractionsHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":5`) {
		t.Fatalf("deferred response = %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"flags":64`) {
		t.Fatalf("deferred response should be ephemeral: %s", rr.Body.String())
	}
	if got := waitReq(t, gotReq); got.Prompt != "why is checkout slow?" {
		t.Fatalf("runner prompt = %q", got.Prompt)
	}
}

func TestDiscordDeliveryHonorsPrivateAndChannelVisibility(t *testing.T) {
	got := make(chan map[string]any, 2)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		got <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	delivery := DiscordWebhookDelivery{Base: api.URL}
	target := ReplyTarget{Platform: PlatformDiscord, ApplicationID: "app1", InteractionToken: "tok"}
	if err := delivery.Deliver(context.Background(), target, Message{Text: "private"}); err != nil {
		t.Fatalf("private Deliver: %v", err)
	}
	if err := delivery.Deliver(context.Background(), target, Message{Text: "channel", Visibility: VisibilityChannel}); err != nil {
		t.Fatalf("channel Deliver: %v", err)
	}

	privatePayload := <-got
	if privatePayload["flags"] != float64(64) {
		t.Fatalf("private payload = %#v, want flags 64", privatePayload)
	}
	channelPayload := <-got
	if _, ok := channelPayload["flags"]; ok {
		t.Fatalf("channel payload should not be ephemeral: %#v", channelPayload)
	}
}

func TestTelegramWebhookSecretAndPollingHappyPath(t *testing.T) {
	gotReq := make(chan RunRequest, 2)
	gotMsg := make(chan Message, 2)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(_ context.Context, req RunRequest) (RunResult, error) {
		gotReq <- req
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), DeliveryFunc(func(_ context.Context, _ ReplyTarget, msg Message) error {
		gotMsg <- msg
		return nil
	}), NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	body := []byte(`{"update_id":10,"message":{"message_id":1,"text":"/ask why pods pending","chat":{"id":42,"type":"private"},"from":{"id":99}}}`)
	adapter := TelegramHTTPAdapter{WebhookSecret: "secret", Service: service}
	bad := httptest.NewRequest(http.MethodPost, "/chatops/telegram/webhook", bytes.NewReader(body))
	bad.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	rr := httptest.NewRecorder()
	adapter.WebhookHandler().ServeHTTP(rr, bad)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad secret status = %d, want 401", rr.Code)
	}

	good := httptest.NewRequest(http.MethodPost, "/chatops/telegram/webhook", bytes.NewReader(body))
	good.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret")
	rr = httptest.NewRecorder()
	adapter.WebhookHandler().ServeHTTP(rr, good)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := waitReq(t, gotReq); got.Prompt != "why pods pending" {
		t.Fatalf("webhook prompt = %q", got.Prompt)
	}
	_ = waitMsg(t, gotMsg)

	pollCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollCalls++
		if !strings.Contains(r.URL.Path, "/botTOKEN/getUpdates") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":11,"message":{"message_id":2,"text":"/ask cpu?","chat":{"id":42,"type":"private"},"from":{"id":99}}}]}`))
	}))
	defer api.Close()

	poller := &TelegramPoller{BaseURL: api.URL, BotToken: "TOKEN", Service: service}
	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := waitReq(t, gotReq); got.Prompt != "cpu?" {
		t.Fatalf("poll prompt = %q", got.Prompt)
	}
	_ = waitMsg(t, gotMsg)
	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("second PollOnce: %v", err)
	}
	select {
	case got := <-gotReq:
		t.Fatalf("duplicate update unexpectedly ran prompt %q", got.Prompt)
	case <-time.After(50 * time.Millisecond):
	}
	if pollCalls != 2 {
		t.Fatalf("pollCalls = %d, want 2 Bot API polls", pollCalls)
	}
}

func TestTelegramGroupRequiresExplicitCommand(t *testing.T) {
	bare := TelegramUpdate{
		UpdateID: 1,
		Message: TelegramMessage{
			MessageID: 1,
			Text:      "ordinary group chatter",
			Chat:      TelegramChat{ID: 42, Type: "group"},
			From:      TelegramUser{ID: 99},
		},
	}
	if _, ok := EventFromTelegramUpdate(bare, TransportHTTP, time.Now()); ok {
		t.Fatal("bare Telegram group text should be ignored")
	}

	commanded := bare
	commanded.UpdateID = 2
	commanded.Message.Text = "/ask why pods pending?"
	ev, ok := EventFromTelegramUpdate(commanded, TransportHTTP, time.Now())
	if !ok {
		t.Fatal("Telegram group /ask should be accepted")
	}
	if ev.Text != "why pods pending?" {
		t.Fatalf("Telegram group prompt = %q", ev.Text)
	}
}

func TestTelegramPollerDoesNotAdvanceOffsetOnAdmissionFailure(t *testing.T) {
	service := NewService(testChatOpsConfig(), nil, nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	shutdownService(t, service)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":21,"message":{"message_id":2,"text":"/ask cpu?","chat":{"id":42,"type":"private"},"from":{"id":99}}}]}`))
	}))
	defer api.Close()

	poller := &TelegramPoller{BaseURL: api.URL, BotToken: "TOKEN", Service: service}
	if err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce returned nil error for closed service")
	}
	if poller.Offset != 0 {
		t.Fatalf("Offset = %d, want unchanged after failed admission", poller.Offset)
	}
}
