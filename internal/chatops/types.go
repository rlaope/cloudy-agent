// Package chatops implements transport-neutral chat platform ingress for Cloudy.
package chatops

import (
	"context"
	"errors"
	"time"
)

const (
	PlatformSlack    = "slack"
	PlatformDiscord  = "discord"
	PlatformTelegram = "telegram"

	TransportHTTP    = "http"
	TransportPolling = "polling"

	VisibilityPrivate = "private"
	VisibilityChannel = "channel"
)

var (
	ErrUnauthenticated = errors.New("chatops: unauthenticated event")
	ErrUnauthorized    = errors.New("chatops: unauthorized source")
	ErrRateLimited     = errors.New("chatops: rate limited")
	ErrQueueFull       = errors.New("chatops: queue full")
	ErrDuplicate       = errors.New("chatops: duplicate event")
)

// Event is the normalized, platform-neutral request shape emitted by adapters
// only after platform-specific verification has succeeded.
type Event struct {
	Platform       string
	Transport      string
	Authenticated  bool
	WorkspaceID    string
	GuildID        string
	ChatID         string
	ChannelID      string
	ThreadID       string
	UserID         string
	Command        string
	Text           string
	Visibility     string
	ReplyTarget    ReplyTarget
	IdempotencyKey string
	ReceivedAt     time.Time
	Meta           map[string]string
}

// ReplyTarget carries the minimum routing data a delivery client needs.
type ReplyTarget struct {
	Platform         string
	WorkspaceID      string
	GuildID          string
	ChatID           string
	ChannelID        string
	ThreadID         string
	UserID           string
	ResponseURL      string
	InteractionToken string
	ApplicationID    string
	MessageID        string
}

// Message is a platform-neutral outbound chat response.
type Message struct {
	Text       string
	Visibility string
	SessionID  string
	Error      bool
}

// Runner executes one Cloudy prompt. Production wraps internal/core/runner;
// tests use fakes to prove authorization and queue behavior.
type Runner interface {
	RunCloudy(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunRequest is the ChatOps subset of a Cloudy runner request.
type RunRequest struct {
	Prompt      string
	SkillName   string
	ProfileName string
	ResumeID    string
	Platform    string
	Source      string
}

// RunResult is the text and session metadata produced by a Cloudy run.
type RunResult struct {
	Text      string
	Model     string
	SessionID string
	Masker    TextMasker
}

// TextMasker is the redaction seam shared by chat replies and session logs.
type TextMasker interface {
	MaskString(string) string
}

// Delivery sends the final response through a chat platform.
type Delivery interface {
	Deliver(ctx context.Context, target ReplyTarget, msg Message) error
}

// DeliveryFunc adapts a function into Delivery.
type DeliveryFunc func(context.Context, ReplyTarget, Message) error

func (f DeliveryFunc) Deliver(ctx context.Context, target ReplyTarget, msg Message) error {
	return f(ctx, target, msg)
}
