package chatops

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxDiscordBodyBytes = 1 << 20
const discordSignatureWindow = 5 * time.Minute

const (
	discordInteractionPing               = 1
	discordInteractionApplicationCommand = 2
	discordResponsePong                  = 1
	discordResponseDeferredMessage       = 5
)

// ParseDiscordPublicKey decodes the hex Ed25519 public key Discord uses to
// sign interaction requests.
func ParseDiscordPublicKey(hexValue string) (ed25519.PublicKey, error) {
	if hexValue == "" {
		return nil, fmt.Errorf("empty public key")
	}
	b, err := hex.DecodeString(strings.TrimSpace(hexValue))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length %d", len(b))
	}
	return ed25519.PublicKey(b), nil
}

// DiscordHTTPAdapter handles Discord interaction webhooks.
type DiscordHTTPAdapter struct {
	PublicKey     ed25519.PublicKey
	ApplicationID string
	Service       *Service
	Now           func() time.Time
}

// InteractionsHandler returns an HTTP handler for /chatops/discord/interactions.
func (a DiscordHTTPAdapter) InteractionsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.readAndVerify(w, r)
		if !ok {
			return
		}
		var payload discordInteraction
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if payload.Type == discordInteractionPing {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": discordResponsePong})
			return
		}
		text := payload.Data.OptionValue("question")
		if text == "" {
			text = payload.Data.OptionValue("text")
		}
		ev := Event{
			Platform:       PlatformDiscord,
			Transport:      TransportHTTP,
			Authenticated:  true,
			GuildID:        payload.GuildID,
			ChannelID:      payload.ChannelID,
			UserID:         payload.UserID(),
			Command:        payload.Data.Name,
			Text:           strings.TrimSpace(text),
			ReplyTarget:    ReplyTarget{Platform: PlatformDiscord, GuildID: payload.GuildID, ChannelID: payload.ChannelID, InteractionToken: payload.Token, ApplicationID: firstNonEmpty(a.ApplicationID, payload.ApplicationID)},
			IdempotencyKey: payload.ID,
			ReceivedAt:     a.now().UTC(),
		}
		if err := a.Service.Handle(r.Context(), ev); err != nil && !isAcceptedNoRun(err) {
			_ = json.NewEncoder(w).Encode(discordEphemeral("Cloudy is not allowed to run for this Discord source."))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": discordResponseDeferredMessage,
			"data": map[string]any{"flags": 64},
		})
	})
}

func (a DiscordHTTPAdapter) readAndVerify(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, tooLarge, err := readBoundedBody(r.Body, maxDiscordBodyBytes)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, false
	}
	if tooLarge {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	timestamp := r.Header.Get("X-Signature-Timestamp")
	if !freshDiscordTimestamp(timestamp, a.now()) || !VerifyDiscordSignature(a.PublicKey, timestamp, body, r.Header.Get("X-Signature-Ed25519")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (a DiscordHTTPAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func freshDiscordTimestamp(timestamp string, now time.Time) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	diff := now.Sub(time.Unix(ts, 0))
	return diff <= discordSignatureWindow && diff >= -discordSignatureWindow
}

// VerifyDiscordSignature validates Ed25519(timestamp + rawBody).
func VerifyDiscordSignature(publicKey ed25519.PublicKey, timestamp string, body []byte, signatureHex string) bool {
	if len(publicKey) != ed25519.PublicKeySize || timestamp == "" || signatureHex == "" {
		return false
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := append([]byte(timestamp), body...)
	return ed25519.Verify(publicKey, msg, sig)
}

type discordInteraction struct {
	ID            string      `json:"id"`
	ApplicationID string      `json:"application_id"`
	Type          int         `json:"type"`
	Token         string      `json:"token"`
	GuildID       string      `json:"guild_id"`
	ChannelID     string      `json:"channel_id"`
	Member        discordUser `json:"member"`
	User          discordUser `json:"user"`
	Data          discordData `json:"data"`
}

func (i discordInteraction) UserID() string {
	if i.Member.User.ID != "" {
		return i.Member.User.ID
	}
	return i.User.ID
}

type discordUser struct {
	ID   string      `json:"id"`
	User discordBare `json:"user"`
}

type discordBare struct {
	ID string `json:"id"`
}

type discordData struct {
	Name    string          `json:"name"`
	Options []discordOption `json:"options"`
}

func (d discordData) OptionValue(name string) string {
	for _, opt := range d.Options {
		if opt.Name == name {
			return opt.Value
		}
	}
	return ""
}

type discordOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func discordEphemeral(text string) map[string]any {
	return map[string]any{
		"type": 4,
		"data": map[string]any{
			"content": text,
			"flags":   64,
		},
	}
}

// DiscordWebhookDelivery sends followup messages with an interaction token.
type DiscordWebhookDelivery struct {
	Client *http.Client
	Base   string
}

func (d DiscordWebhookDelivery) Deliver(ctx context.Context, target ReplyTarget, msg Message) error {
	if target.Platform != PlatformDiscord || target.InteractionToken == "" || target.ApplicationID == "" {
		return nil
	}
	client := d.Client
	base := strings.TrimRight(d.Base, "/")
	if base == "" {
		base = "https://discord.com/api/v10"
	}
	url := fmt.Sprintf("%s/webhooks/%s/%s", base, target.ApplicationID, target.InteractionToken)
	payload := map[string]any{
		"content":          FormatMessage(PlatformDiscord, msg),
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	if msg.Visibility != VisibilityChannel {
		payload["flags"] = 64
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discord delivery: invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doChatOpsRequest(client, req, "discord delivery")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord delivery: status %d", resp.StatusCode)
	}
	return nil
}
