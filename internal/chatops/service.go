package chatops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/session"
)

const (
	idempotencyTTL        = 24 * time.Hour
	maxIdempotencyEntries = 10000
)

// Service owns transport-neutral admission, idempotency, queueing, runner
// invocation, session mapping, and delivery.
type Service struct {
	policy   *Policy
	queue    *Queue
	sessions *SessionMap

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewService constructs a ChatOps service with bounded asynchronous workers.
func NewService(cfg config.ChatOpsConfig, runner Runner, delivery Delivery, sessions *SessionMap) *Service {
	if sessions == nil {
		sessions = NewSessionMap("")
	}
	policy := NewPolicy(cfg)
	queue := NewQueue(cfg, runner, delivery, sessions)
	return &Service{
		policy:   policy,
		queue:    queue,
		sessions: sessions,
		seen:     map[string]time.Time{},
	}
}

// Handle validates a normalized event and enqueues one Cloudy run. It returns
// once the job is accepted, not after the LLM run completes.
func (s *Service) Handle(ctx context.Context, ev Event) error {
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = time.Now().UTC()
	}
	decision, err := s.policy.Authorize(ev)
	if err != nil {
		return err
	}
	marked := false
	if ev.IdempotencyKey != "" {
		if !s.markSeen(ev.IdempotencyKey, ev.ReceivedAt) {
			return ErrDuplicate
		}
		marked = true
	}
	if err := s.queue.Enqueue(ctx, Job{Event: ev, Decision: decision}); err != nil {
		if marked {
			s.unmarkSeen(ev.IdempotencyKey)
		}
		return err
	}
	return nil
}

// Shutdown stops queue workers after in-flight jobs finish or ctx cancels.
func (s *Service) Shutdown(ctx context.Context) error {
	return s.queue.Shutdown(ctx)
}

func (s *Service) markSeen(key string, now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSeenLocked(now)
	if _, ok := s.seen[key]; ok {
		return false
	}
	if len(s.seen) >= maxIdempotencyEntries {
		for k := range s.seen {
			delete(s.seen, k)
			if len(s.seen) < maxIdempotencyEntries {
				break
			}
		}
	}
	s.seen[key] = now
	return true
}

func (s *Service) unmarkSeen(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, key)
}

func (s *Service) pruneSeenLocked(now time.Time) {
	cutoff := now.Add(-idempotencyTTL)
	for key, seenAt := range s.seen {
		if seenAt.Before(cutoff) {
			delete(s.seen, key)
		}
	}
}

// Job is one accepted ChatOps request waiting for or running through Cloudy.
type Job struct {
	Event    Event
	Decision Decision
}

// Queue runs accepted jobs asynchronously with bounded concurrency.
type Queue struct {
	runner   Runner
	delivery Delivery
	sessions *SessionMap

	jobs            chan Job
	timeout         time.Duration
	discordTimeout  time.Duration
	deliveryTimeout time.Duration

	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
}

// NewQueue creates worker goroutines immediately.
func NewQueue(cfg config.ChatOpsConfig, runner Runner, delivery Delivery, sessions *SessionMap) *Queue {
	if sessions == nil {
		sessions = NewSessionMap("")
	}
	if runner == nil {
		runner = RunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
			return RunResult{}, errors.New("chatops: runner not configured")
		})
	}
	if delivery == nil {
		delivery = DeliveryFunc(func(context.Context, ReplyTarget, Message) error { return nil })
	}
	workers := cfg.MaxConcurrentRuns
	if workers <= 0 {
		workers = 1
	}
	depth := cfg.Queue.MaxDepth
	if depth <= 0 {
		depth = 64
	}
	timeoutSeconds := cfg.Queue.DefaultTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	discordTimeoutSeconds := cfg.Queue.DiscordTimeoutSeconds
	if discordTimeoutSeconds <= 0 {
		discordTimeoutSeconds = timeoutSeconds
	}
	deliveryTimeoutSeconds := cfg.Queue.DeliveryTimeoutSeconds
	if deliveryTimeoutSeconds <= 0 {
		deliveryTimeoutSeconds = 30
	}

	q := &Queue{
		runner:          runner,
		delivery:        delivery,
		sessions:        sessions,
		jobs:            make(chan Job, depth),
		timeout:         time.Duration(timeoutSeconds) * time.Second,
		discordTimeout:  time.Duration(discordTimeoutSeconds) * time.Second,
		deliveryTimeout: time.Duration(deliveryTimeoutSeconds) * time.Second,
	}
	for range workers {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Enqueue accepts a job without waiting for the runner.
func (q *Queue) Enqueue(ctx context.Context, job Job) error {
	q.closeMu.Lock()
	defer q.closeMu.Unlock()
	if q.closed {
		return fmt.Errorf("chatops: queue closed")
	}
	select {
	case q.jobs <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

// Shutdown drains workers after closing the queue.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.closeMu.Lock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
	q.closeMu.Unlock()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for job := range q.jobs {
		q.run(job)
	}
}

func (q *Queue) run(job Job) {
	ev := job.Event
	resumeID := q.sessions.Lookup(ev)
	ctx, cancel := context.WithTimeout(context.Background(), q.timeoutFor(ev.Platform))
	defer cancel()

	result, err := q.runner.RunCloudy(ctx, RunRequest{
		Prompt:      ev.Text,
		SkillName:   job.Decision.Skill,
		ProfileName: job.Decision.Profile,
		ResumeID:    resumeID,
		Platform:    ev.Platform,
		Source:      ev.SourceKey(),
	})
	msg := Message{Visibility: job.Decision.Visibility}
	if err != nil {
		msg.Text = "Cloudy failed to answer this request safely."
		msg.Error = true
	} else {
		msg.Text = result.Text
		msg.SessionID = result.SessionID
		if result.SessionID != "" {
			_ = q.sessions.Remember(ev, result.SessionID)
		}
	}
	deliveryCtx, deliveryCancel := context.WithTimeout(context.Background(), q.deliveryTimeout)
	defer deliveryCancel()
	if err := q.delivery.Deliver(deliveryCtx, ev.ReplyTarget, msg); err != nil {
		q.recordDeliveryFailure(msg.SessionID, ev, err, result.Masker)
	}
}

func (q *Queue) timeoutFor(platform string) time.Duration {
	if platform == PlatformDiscord && q.discordTimeout > 0 {
		return q.discordTimeout
	}
	return q.timeout
}

func (q *Queue) recordDeliveryFailure(sessionID string, ev Event, err error, masker TextMasker) {
	if sessionID == "" || err == nil {
		return
	}
	if masker == nil {
		masker = permission.MaskerOrDefault(nil)
	}
	sess, openErr := session.New(sessionID)
	if openErr != nil {
		return
	}
	defer func() { _ = sess.Close() }()
	_ = sess.Append(session.Event{
		Kind: session.KindError,
		Name: "chatops.delivery_failed",
		Text: masker.MaskString(err.Error()),
		Meta: map[string]any{
			"platform": ev.Platform,
			"source":   ev.SourceKey(),
		},
	})
}

// RunnerFunc adapts a function into Runner.
type RunnerFunc func(context.Context, RunRequest) (RunResult, error)

func (f RunnerFunc) RunCloudy(ctx context.Context, req RunRequest) (RunResult, error) {
	return f(ctx, req)
}
