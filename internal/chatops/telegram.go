package chatops

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxTelegramBodyBytes = 1 << 20

// TelegramHTTPAdapter handles Telegram webhooks.
type TelegramHTTPAdapter struct {
	WebhookSecret string
	Service       *Service
	Now           func() time.Time
}

// WebhookHandler returns an HTTP handler for /chatops/telegram/webhook.
func (a TelegramHTTPAdapter) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.WebhookSecret != "" && !constantTimeEqual(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"), a.WebhookSecret) {
			http.Error(w, "invalid secret", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxTelegramBodyBytes))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var update TelegramUpdate
		if err := json.Unmarshal(body, &update); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		ev, ok := EventFromTelegramUpdate(update, TransportHTTP, a.now())
		if ok {
			ev.Authenticated = true
			if err := a.Service.Handle(r.Context(), ev); err != nil && !isAcceptedNoRun(err) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "ignored")
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
}

func constantTimeEqual(got, want string) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (a TelegramHTTPAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// TelegramUpdate is the subset of Bot API update data Cloudy needs.
type TelegramUpdate struct {
	UpdateID int64           `json:"update_id"`
	Message  TelegramMessage `json:"message"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Text      string       `json:"text"`
	Chat      TelegramChat `json:"chat"`
	From      TelegramUser `json:"from"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type TelegramUser struct {
	ID int64 `json:"id"`
}

// EventFromTelegramUpdate normalizes a Telegram update into a ChatOps event.
func EventFromTelegramUpdate(update TelegramUpdate, transport string, now time.Time) (Event, bool) {
	text := strings.TrimSpace(update.Message.Text)
	if text == "" || update.Message.Chat.ID == 0 {
		return Event{}, false
	}
	command := ""
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		command = strings.TrimPrefix(parts[0], "/")
		if at := strings.Index(command, "@"); at >= 0 {
			command = command[:at]
		}
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(update.Message.Text, parts[0]), " "))
	}
	if command == "" && update.Message.Chat.Type != "private" {
		return Event{}, false
	}
	if command != "" && command != "ask" && command != "new" && command != "resume" && command != "status" {
		return Event{}, false
	}
	chatID := strconv.FormatInt(update.Message.Chat.ID, 10)
	userID := strconv.FormatInt(update.Message.From.ID, 10)
	return Event{
		Platform:       PlatformTelegram,
		Transport:      transport,
		Authenticated:  transport == TransportPolling,
		ChatID:         chatID,
		ChannelID:      chatID,
		UserID:         userID,
		Command:        defaultString(command, "text"),
		Text:           stripAskPrefix(text),
		ReplyTarget:    ReplyTarget{Platform: PlatformTelegram, ChatID: chatID, MessageID: strconv.FormatInt(update.Message.MessageID, 10)},
		IdempotencyKey: fmt.Sprintf("telegram:%d", update.UpdateID),
		ReceivedAt:     now.UTC(),
	}, true
}

// TelegramPoller reads getUpdates and feeds the same Service as webhooks.
type TelegramPoller struct {
	BaseURL   string
	BotToken  string
	Service   *Service
	Client    *http.Client
	Offset    int64
	Now       func() time.Time
	LongPollS int
}

// PollOnce fetches one getUpdates batch from a Bot API server.
func (p *TelegramPoller) PollOnce(ctx context.Context) error {
	client := p.Client
	u := strings.TrimRight(p.BaseURL, "/") + "/bot" + p.BotToken + "/getUpdates"
	q := url.Values{}
	if p.Offset > 0 {
		q.Set("offset", strconv.FormatInt(p.Offset, 10))
	}
	q.Set("timeout", strconv.Itoa(p.LongPollS))
	if p.LongPollS == 0 {
		q.Set("timeout", "0")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("telegram polling: invalid Bot API URL")
	}
	resp, err := doChatOpsRequest(client, req, "telegram polling")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram polling: status %d", resp.StatusCode)
	}
	var payload struct {
		OK     bool             `json:"ok"`
		Result []TelegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("telegram polling: bot api returned ok=false")
	}
	for _, update := range payload.Result {
		if update.UpdateID < p.Offset {
			continue
		}
		ev, ok := EventFromTelegramUpdate(update, TransportPolling, p.now())
		if !ok {
			p.advanceOffset(update.UpdateID)
			continue
		}
		if err := p.Service.Handle(ctx, ev); err != nil {
			if isAcceptedNoRun(err) {
				p.advanceOffset(update.UpdateID)
				continue
			}
			return err
		}
		p.advanceOffset(update.UpdateID)
	}
	return nil
}

func (p *TelegramPoller) advanceOffset(updateID int64) {
	if updateID >= p.Offset {
		p.Offset = updateID + 1
	}
}

func (p *TelegramPoller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// TelegramDelivery sends sendMessage requests through the Bot API.
type TelegramDelivery struct {
	BaseURL  string
	BotToken string
	Client   *http.Client
}

func (d TelegramDelivery) Deliver(ctx context.Context, target ReplyTarget, msg Message) error {
	if target.Platform != PlatformTelegram || target.ChatID == "" || d.BotToken == "" {
		return nil
	}
	client := d.Client
	base := strings.TrimRight(d.BaseURL, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	u := base + "/bot" + d.BotToken + "/sendMessage"
	text := FormatMessage(PlatformTelegram, msg)
	for _, chunk := range ChunkText(text, 3900) {
		payload := map[string]any{
			"chat_id":    target.ChatID,
			"text":       chunk,
			"parse_mode": "MarkdownV2",
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("telegram delivery: invalid Bot API URL")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := doChatOpsRequest(client, req, "telegram delivery")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("telegram delivery: status %d", resp.StatusCode)
		}
	}
	return nil
}
