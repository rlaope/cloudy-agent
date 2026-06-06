package chatops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxSlackBodyBytes = 1 << 20

// SlackHTTPAdapter handles Slack slash commands and Events API requests.
type SlackHTTPAdapter struct {
	SigningSecret string
	Service       *Service
	Now           func() time.Time
}

// SlashCommandHandler returns an HTTP handler for /chatops/slack/commands.
func (a SlackHTTPAdapter) SlashCommandHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.readAndVerify(w, r)
		if !ok {
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(form.Get("text"))
		ev := Event{
			Platform:       PlatformSlack,
			Transport:      TransportHTTP,
			Authenticated:  true,
			WorkspaceID:    form.Get("team_id"),
			ChannelID:      form.Get("channel_id"),
			UserID:         form.Get("user_id"),
			Command:        form.Get("command"),
			Text:           stripAskPrefix(text),
			ReplyTarget:    slackReplyTarget(form),
			IdempotencyKey: firstNonEmpty(form.Get("trigger_id"), form.Get("response_url")),
			ReceivedAt:     a.now().UTC(),
			Meta:           map[string]string{"team_domain": form.Get("team_domain")},
		}
		if err := a.Service.Handle(r.Context(), ev); err != nil && !isAcceptedNoRun(err) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "Cloudy is not allowed to run for this Slack source.")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Cloudy is working on it.")
	})
}

// EventsHandler returns an HTTP handler for /chatops/slack/events.
func (a SlackHTTPAdapter) EventsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.readAndVerify(w, r)
		if !ok {
			return
		}
		var payload struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
			TeamID    string `json:"team_id"`
			EventID   string `json:"event_id"`
			Event     struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				User    string `json:"user"`
				Channel string `json:"channel"`
				Thread  string `json:"thread_ts"`
				TS      string `json:"ts"`
			} `json:"event"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if payload.Type == "url_verification" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, payload.Challenge)
			return
		}
		if payload.Event.Type != "app_mention" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ignored")
			return
		}
		ev := Event{
			Platform:       PlatformSlack,
			Transport:      TransportHTTP,
			Authenticated:  true,
			WorkspaceID:    payload.TeamID,
			ChannelID:      payload.Event.Channel,
			ThreadID:       payload.Event.Thread,
			UserID:         payload.Event.User,
			Command:        payload.Event.Type,
			Text:           stripMention(payload.Event.Text),
			ReplyTarget:    ReplyTarget{Platform: PlatformSlack, WorkspaceID: payload.TeamID, ChannelID: payload.Event.Channel, ThreadID: firstNonEmpty(payload.Event.Thread, payload.Event.TS), UserID: payload.Event.User},
			IdempotencyKey: firstNonEmpty(payload.EventID, payload.Event.TS),
			ReceivedAt:     a.now().UTC(),
		}
		if err := a.Service.Handle(r.Context(), ev); err != nil && !isAcceptedNoRun(err) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ignored")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
}

func (a SlackHTTPAdapter) readAndVerify(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSlackBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, false
	}
	if !VerifySlackSignature(a.SigningSecret, r.Header.Get("X-Slack-Request-Timestamp"), body, r.Header.Get("X-Slack-Signature"), a.now()) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (a SlackHTTPAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// VerifySlackSignature validates Slack's v0 HMAC signature over the raw body.
func VerifySlackSignature(secret, timestamp string, body []byte, signature string, now time.Time) bool {
	if secret == "" || timestamp == "" || signature == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if diff := now.Sub(time.Unix(ts, 0)); diff > 5*time.Minute || diff < -5*time.Minute {
		return false
	}
	if !strings.HasPrefix(signature, "v0=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "v0:%s:", timestamp)
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "v0="))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

func slackReplyTarget(form url.Values) ReplyTarget {
	return ReplyTarget{
		Platform:    PlatformSlack,
		WorkspaceID: form.Get("team_id"),
		ChannelID:   form.Get("channel_id"),
		UserID:      form.Get("user_id"),
		ResponseURL: form.Get("response_url"),
	}
}

func stripAskPrefix(text string) string {
	text = strings.TrimSpace(text)
	for _, prefix := range []string{"ask ", "/ask "} {
		if strings.HasPrefix(strings.ToLower(text), prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	return text
}

func stripMention(text string) string {
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "<@") {
		return strings.Join(fields[1:], " ")
	}
	return text
}

func isAcceptedNoRun(err error) bool {
	return err == ErrDuplicate
}

// SignSlackTestBody is used by tests and examples to build valid fixtures.
func SignSlackTestBody(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "v0:%s:", timestamp)
	_, _ = mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// SlackDelivery posts through Slack response_url endpoints or, for Events API
// app mentions, through bot-token chat.postEphemeral/chat.postMessage calls.
type SlackDelivery struct {
	Client   *http.Client
	BotToken string
	BaseURL  string
}

func (d SlackDelivery) Deliver(ctx context.Context, target ReplyTarget, msg Message) error {
	if target.Platform != PlatformSlack {
		return nil
	}
	if target.ResponseURL != "" {
		return d.deliverResponseURL(ctx, target, msg)
	}
	return d.deliverBotMessage(ctx, target, msg)
}

func (d SlackDelivery) deliverResponseURL(ctx context.Context, target ReplyTarget, msg Message) error {
	client := d.Client
	payload := map[string]any{
		"text":          FormatMessage(PlatformSlack, msg),
		"response_type": "ephemeral",
	}
	if msg.Visibility == VisibilityChannel {
		payload["response_type"] = "in_channel"
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.ResponseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack delivery: invalid response URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doChatOpsRequest(client, req, "slack delivery")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack delivery: status %d", resp.StatusCode)
	}
	return nil
}

func (d SlackDelivery) deliverBotMessage(ctx context.Context, target ReplyTarget, msg Message) error {
	if target.ChannelID == "" || d.BotToken == "" {
		return fmt.Errorf("slack delivery: bot token and channel are required for message delivery")
	}
	client := d.Client
	base := strings.TrimRight(d.BaseURL, "/")
	if base == "" {
		base = "https://slack.com/api"
	}
	endpoint := "chat.postMessage"
	payload := map[string]any{
		"channel": target.ChannelID,
		"text":    FormatMessage(PlatformSlack, msg),
		"mrkdwn":  true,
	}
	if msg.Visibility != VisibilityChannel {
		if target.UserID == "" {
			return fmt.Errorf("slack delivery: user is required for private message delivery")
		}
		endpoint = "chat.postEphemeral"
		payload["user"] = target.UserID
	}
	if target.ThreadID != "" {
		payload["thread_ts"] = target.ThreadID
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack delivery: invalid API URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.BotToken)
	resp, err := doChatOpsRequest(client, req, "slack delivery")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack delivery: status %d", resp.StatusCode)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("slack delivery: %s", result.Error)
	}
	return nil
}
