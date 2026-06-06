package chatops

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

const maxRateWindows = 10000

// Decision is the resolved policy for one authorized event.
type Decision struct {
	Profile    string
	Skill      string
	Visibility string
}

// Policy enforces platform allowlists, routing, and coarse source rate limits.
type Policy struct {
	cfg config.ChatOpsConfig

	mu      sync.Mutex
	windows map[string]rateWindow
	now     func() time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

// NewPolicy builds an admission policy from config.
func NewPolicy(cfg config.ChatOpsConfig) *Policy {
	return &Policy{
		cfg:     cfg,
		windows: map[string]rateWindow{},
		now:     time.Now,
	}
}

// Authorize returns the effective routing decision for an event or a stable
// admission error. It never starts provider, tool, or delivery work.
func (p *Policy) Authorize(ev Event) (Decision, error) {
	if !ev.Authenticated {
		return Decision{}, ErrUnauthenticated
	}
	if strings.TrimSpace(ev.Text) == "" {
		return Decision{}, fmt.Errorf("chatops: empty prompt")
	}
	if err := p.checkAllowlist(ev); err != nil {
		return Decision{}, err
	}
	if err := p.checkRate(ev); err != nil {
		return Decision{}, err
	}

	decision := Decision{
		Profile:    p.cfg.DefaultProfile,
		Visibility: defaultString(ev.Visibility, p.cfg.DefaultVisibility, VisibilityPrivate),
	}
	for _, route := range p.cfg.Routes {
		if routeMatches(route, ev) {
			if route.Profile != "" {
				decision.Profile = route.Profile
			}
			if route.Skill != "" {
				decision.Skill = route.Skill
			}
			if route.Visibility != "" {
				decision.Visibility = route.Visibility
			}
			break
		}
	}
	return decision, nil
}

func (p *Policy) checkAllowlist(ev Event) error {
	switch ev.Platform {
	case PlatformSlack:
		c := p.cfg.Platforms.Slack
		if !c.Enabled {
			return ErrUnauthorized
		}
		if len(c.AllowedTeamIDs) == 0 || (len(c.AllowedChannelIDs) == 0 && len(c.AllowedUserIDs) == 0) {
			return ErrUnauthorized
		}
		if !allowed(c.AllowedTeamIDs, ev.WorkspaceID) ||
			!allowed(c.AllowedChannelIDs, ev.ChannelID) ||
			!allowed(c.AllowedUserIDs, ev.UserID) {
			return ErrUnauthorized
		}
	case PlatformDiscord:
		c := p.cfg.Platforms.Discord
		if !c.Enabled {
			return ErrUnauthorized
		}
		if len(c.AllowedGuildIDs) == 0 || (len(c.AllowedChannelIDs) == 0 && len(c.AllowedUserIDs) == 0) {
			return ErrUnauthorized
		}
		if !allowed(c.AllowedGuildIDs, ev.GuildID) ||
			!allowed(c.AllowedChannelIDs, ev.ChannelID) ||
			!allowed(c.AllowedUserIDs, ev.UserID) {
			return ErrUnauthorized
		}
	case PlatformTelegram:
		c := p.cfg.Platforms.Telegram
		if !c.Enabled {
			return ErrUnauthorized
		}
		if len(c.AllowedChatIDs) == 0 {
			return ErrUnauthorized
		}
		if !allowed(c.AllowedChatIDs, ev.ChatID) ||
			!allowed(c.AllowedUserIDs, ev.UserID) {
			return ErrUnauthorized
		}
	default:
		return ErrUnauthorized
	}
	return nil
}

func (p *Policy) checkRate(ev Event) error {
	limit := p.cfg.RateLimit.PerSourcePerMinute
	if limit <= 0 {
		return nil
	}
	key := ev.SourceKey()
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneRateWindowsLocked(now)
	if len(p.windows) >= maxRateWindows {
		for k := range p.windows {
			delete(p.windows, k)
			if len(p.windows) < maxRateWindows {
				break
			}
		}
	}
	w := p.windows[key]
	if w.start.IsZero() || now.Sub(w.start) >= time.Minute {
		p.windows[key] = rateWindow{start: now, count: 1}
		return nil
	}
	if w.count >= limit {
		return ErrRateLimited
	}
	w.count++
	p.windows[key] = w
	return nil
}

func (p *Policy) pruneRateWindowsLocked(now time.Time) {
	for key, window := range p.windows {
		if !window.start.IsZero() && now.Sub(window.start) >= time.Minute {
			delete(p.windows, key)
		}
	}
}

// SourceKey returns a stable policy/rate-limit key for the inbound source.
func (ev Event) SourceKey() string {
	return strings.Join([]string{
		ev.Platform,
		firstNonEmpty(ev.WorkspaceID, ev.GuildID, ev.ChatID),
		ev.ChannelID,
		ev.UserID,
	}, "|")
}

func routeMatches(route config.ChatOpsRoute, ev Event) bool {
	return match(route.Platform, ev.Platform) &&
		match(route.WorkspaceID, ev.WorkspaceID) &&
		match(route.GuildID, ev.GuildID) &&
		match(route.ChatID, ev.ChatID) &&
		match(route.ChannelID, ev.ChannelID) &&
		match(route.UserID, ev.UserID)
}

func allowed(list []string, value string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func match(want, got string) bool { return want == "" || want == got }

func defaultString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
