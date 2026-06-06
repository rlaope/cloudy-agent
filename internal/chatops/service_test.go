package chatops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

type replaceMasker struct {
	old string
	new string
}

func (m replaceMasker) MaskString(s string) string {
	return strings.ReplaceAll(s, m.old, m.new)
}

func testChatOpsConfig() config.ChatOpsConfig {
	cfg := config.Default().ChatOps
	cfg.MaxConcurrentRuns = 1
	cfg.Queue.MaxDepth = 8
	cfg.Platforms.Slack.Enabled = true
	cfg.Platforms.Slack.AllowedTeamIDs = []string{"T1"}
	cfg.Platforms.Slack.AllowedChannelIDs = []string{"C1"}
	cfg.Platforms.Slack.AllowedUserIDs = []string{"U1"}
	cfg.Platforms.Discord.Enabled = true
	cfg.Platforms.Discord.AllowedGuildIDs = []string{"G1"}
	cfg.Platforms.Discord.AllowedChannelIDs = []string{"D1"}
	cfg.Platforms.Discord.AllowedUserIDs = []string{"DU1"}
	cfg.Platforms.Telegram.Enabled = true
	cfg.Platforms.Telegram.AllowedChatIDs = []string{"42"}
	cfg.Platforms.Telegram.AllowedUserIDs = []string{"99"}
	cfg.Routes = []config.ChatOpsRoute{{
		Platform:   PlatformSlack,
		ChannelID:  "C1",
		Profile:    "payments-sre",
		Skill:      "incident-context",
		Visibility: VisibilityPrivate,
	}}
	return cfg
}

func TestEventHandlerRejectsUnauthenticatedWithoutRunner(t *testing.T) {
	called := false
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		called = true
		return RunResult{}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	err := service.Handle(context.Background(), Event{
		Platform:  PlatformSlack,
		Text:      "why is checkout slow?",
		ChannelID: "C1",
		UserID:    "U1",
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Handle error = %v, want ErrUnauthenticated", err)
	}
	if called {
		t.Fatal("runner was called for unauthenticated event")
	}
}

func TestEventHandlerRejectsUnauthorizedChannelWithoutRunner(t *testing.T) {
	called := false
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		called = true
		return RunResult{}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	err := service.Handle(context.Background(), Event{
		Platform:      PlatformSlack,
		Authenticated: true,
		WorkspaceID:   "T1",
		ChannelID:     "C-denied",
		UserID:        "U1",
		Text:          "why is checkout slow?",
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Handle error = %v, want ErrUnauthorized", err)
	}
	if called {
		t.Fatal("runner was called for unauthorized event")
	}
}

func TestEventHandlerRejectsMissingAllowlistScopeWithoutRunner(t *testing.T) {
	cfg := testChatOpsConfig()
	cfg.Platforms.Slack.AllowedTeamIDs = nil
	cfg.Platforms.Slack.AllowedChannelIDs = nil
	cfg.Platforms.Slack.AllowedUserIDs = nil
	called := false
	service := NewService(cfg, RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		called = true
		return RunResult{}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	err := service.Handle(context.Background(), Event{
		Platform:      PlatformSlack,
		Authenticated: true,
		WorkspaceID:   "T1",
		ChannelID:     "C1",
		UserID:        "U1",
		Text:          "why is checkout slow?",
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Handle error = %v, want ErrUnauthorized", err)
	}
	if called {
		t.Fatal("runner was called for event without explicit allowlist scope")
	}
}

func TestEventHandlerMapsRouteToProfileSkillVisibility(t *testing.T) {
	gotReq := make(chan RunRequest, 1)
	gotDelivery := make(chan Message, 1)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(_ context.Context, req RunRequest) (RunResult, error) {
		gotReq <- req
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), DeliveryFunc(func(_ context.Context, _ ReplyTarget, msg Message) error {
		gotDelivery <- msg
		return nil
	}), NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	if err := service.Handle(context.Background(), Event{
		Platform:       PlatformSlack,
		Authenticated:  true,
		WorkspaceID:    "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "why is checkout slow?",
		ReplyTarget:    ReplyTarget{Platform: PlatformSlack},
		IdempotencyKey: "route-1",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	req := waitReq(t, gotReq)
	if req.SkillName != "incident-context" || req.ProfileName != "payments-sre" {
		t.Fatalf("runner route = %#v", req)
	}
	msg := waitMsg(t, gotDelivery)
	if msg.SessionID != "s1" || msg.Visibility != VisibilityPrivate {
		t.Fatalf("delivery message = %#v", msg)
	}
}

func TestQueueAckDoesNotWaitForRunner(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		close(started)
		<-unblock
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	begin := time.Now()
	err := service.Handle(context.Background(), Event{
		Platform:       PlatformSlack,
		Authenticated:  true,
		WorkspaceID:    "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "slow question",
		IdempotencyKey: "slow-1",
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if time.Since(begin) > 100*time.Millisecond {
		t.Fatal("Handle waited for runner")
	}
	<-started
	close(unblock)
}

func TestQueueEnqueueDuringShutdownDoesNotPanic(t *testing.T) {
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))

	done := make(chan struct{})
	panicCh := make(chan any, 50)
	for i := 0; i < 50; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			_ = service.Handle(context.Background(), Event{
				Platform:      PlatformSlack,
				Authenticated: true,
				WorkspaceID:   "T1",
				ChannelID:     "C1",
				UserID:        "U1",
				Text:          "race",
			})
			done <- struct{}{}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i := 0; i < 50; i++ {
		select {
		case <-done:
			select {
			case p := <-panicCh:
				t.Fatalf("handler panicked during shutdown: %v", p)
			default:
			}
		case <-time.After(time.Second):
			t.Fatal("handler goroutine did not return")
		}
	}
}

func TestQueueRecordsMaskedDeliveryFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{Text: "answer", SessionID: "delivery-failure-session"}, nil
	}), DeliveryFunc(func(context.Context, ReplyTarget, Message) error {
		return errors.New("post failed with token xoxb-1234567890-abcdef")
	}), NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	if err := service.Handle(context.Background(), Event{
		Platform:      PlatformSlack,
		Authenticated: true,
		WorkspaceID:   "T1",
		ChannelID:     "C1",
		UserID:        "U1",
		Text:          "delivery fail",
		ReplyTarget: ReplyTarget{
			Platform:    PlatformSlack,
			ResponseURL: "https://slack.example/response",
		},
		IdempotencyKey: "delivery-failure",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	path := filepath.Join(home, "logs", "delivery-failure-session.jsonl")
	deadline := time.After(time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), "chatops.delivery_failed") {
			text := string(b)
			if strings.Contains(text, "xoxb-") {
				t.Fatalf("delivery failure log leaked token: %s", text)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("delivery failure was not logged; last read err=%v", err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestQueueRecordsDeliveryFailureWithRunnerMasker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{
			Text:      "answer",
			SessionID: "profile-delivery-failure-session",
			Masker:    replaceMasker{old: "CORPSECRET-4242", new: "[PROFILE-REDACTED]"},
		}, nil
	}), DeliveryFunc(func(context.Context, ReplyTarget, Message) error {
		return errors.New("post failed with CORPSECRET-4242")
	}), NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	if err := service.Handle(context.Background(), Event{
		Platform:      PlatformSlack,
		Authenticated: true,
		WorkspaceID:   "T1",
		ChannelID:     "C1",
		UserID:        "U1",
		Text:          "delivery fail",
		ReplyTarget: ReplyTarget{
			Platform:    PlatformSlack,
			ResponseURL: "https://slack.example/response",
		},
		IdempotencyKey: "profile-delivery-failure",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	path := filepath.Join(home, "logs", "profile-delivery-failure-session.jsonl")
	deadline := time.After(time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), "chatops.delivery_failed") {
			text := string(b)
			if strings.Contains(text, "CORPSECRET-4242") {
				t.Fatalf("delivery failure log ignored runner masker: %s", text)
			}
			if !strings.Contains(text, "[PROFILE-REDACTED]") {
				t.Fatalf("delivery failure log did not use runner masker: %s", text)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("delivery failure log not written at %s", path)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestServicePrunesExpiredIdempotencyKeys(t *testing.T) {
	service := NewService(testChatOpsConfig(), nil, nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)
	now := time.Now().UTC()
	service.seen["old"] = now.Add(-idempotencyTTL - time.Second)

	if !service.markSeen("new", now) {
		t.Fatal("new idempotency key was treated as duplicate")
	}
	if _, ok := service.seen["old"]; ok {
		t.Fatal("expired idempotency key was not pruned")
	}
}

func TestPolicyPrunesExpiredRateWindows(t *testing.T) {
	cfg := testChatOpsConfig()
	cfg.RateLimit.PerSourcePerMinute = 10
	policy := NewPolicy(cfg)
	now := time.Now().UTC()
	policy.now = func() time.Time { return now }
	policy.windows["old"] = rateWindow{start: now.Add(-2 * time.Minute), count: 1}

	_, err := policy.Authorize(Event{
		Platform:      PlatformSlack,
		Authenticated: true,
		WorkspaceID:   "T1",
		ChannelID:     "C1",
		UserID:        "U1",
		Text:          "why?",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if _, ok := policy.windows["old"]; ok {
		t.Fatal("expired rate-limit window was not pruned")
	}
}

func TestEventHandlerIdempotencySuppressesDuplicateDelivery(t *testing.T) {
	runs := make(chan struct{}, 2)
	service := NewService(testChatOpsConfig(), RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		runs <- struct{}{}
		return RunResult{Text: "answer", SessionID: "s1"}, nil
	}), nil, NewSessionMap(filepath.Join(t.TempDir(), "sessions.json")))
	defer shutdownService(t, service)

	ev := Event{
		Platform:       PlatformSlack,
		Authenticated:  true,
		WorkspaceID:    "T1",
		ChannelID:      "C1",
		UserID:         "U1",
		Text:           "same",
		IdempotencyKey: "dup",
	}
	if err := service.Handle(context.Background(), ev); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := service.Handle(context.Background(), ev); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Handle = %v, want ErrDuplicate", err)
	}
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("runner was not called for first event")
	}
	select {
	case <-runs:
		t.Fatal("runner was called for duplicate event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSessionMapWrites0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewSessionMap(path)
	ev := Event{Platform: PlatformSlack, WorkspaceID: "T1", ChannelID: "C1", ThreadID: "th"}
	if err := m.Remember(ev, "session-1"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if got := m.Lookup(ev); got != "session-1" {
		t.Fatalf("Lookup = %q, want session-1", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestSessionMapSeparatesUsersInSameChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewSessionMap(path)
	ev := Event{Platform: PlatformSlack, WorkspaceID: "T1", ChannelID: "C1", UserID: "U1"}
	if err := m.Remember(ev, "session-u1"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	otherUser := ev
	otherUser.UserID = "U2"
	if got := m.Lookup(otherUser); got != "" {
		t.Fatalf("Lookup for second user = %q, want isolated session", got)
	}
	if got := m.Lookup(ev); got != "session-u1" {
		t.Fatalf("Lookup for first user = %q, want session-u1", got)
	}
}

func TestFormatterChunksAndEscapes(t *testing.T) {
	chunks := ChunkText("abcdef", 2)
	if len(chunks) != 3 || chunks[0] != "ab" || chunks[2] != "ef" {
		t.Fatalf("chunks = %#v", chunks)
	}
	if got := FormatMessage(PlatformDiscord, Message{Text: "@everyone look"}); got == "@everyone look" {
		t.Fatalf("Discord mention was not suppressed: %q", got)
	}
	if got := FormatMessage(PlatformSlack, Message{Text: "<!here> <@U123> look"}); strings.Contains(got, "<!here") || strings.Contains(got, "<@U123") {
		t.Fatalf("Slack mention was not suppressed: %q", got)
	}
	if got := FormatMessage(PlatformTelegram, Message{Text: "a_b"}); got != `a\_b` {
		t.Fatalf("Telegram escape = %q", got)
	}
}

func waitReq(t *testing.T, ch <-chan RunRequest) RunRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner")
		return RunRequest{}
	}
}

func waitMsg(t *testing.T, ch <-chan Message) Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery")
		return Message{}
	}
}

func shutdownService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
